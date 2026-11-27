export interface TransactionSQL {
  exec<T extends Record<string, SqlStorageValue>>(query: string, ...bindings: SqlStorageValue[]): T[];
}

export class HubStorage {
  readonly #storage: DurableObjectStorage;

  constructor(storage: DurableObjectStorage) {
    this.#storage = storage;
  }

  transaction<T>(fn: (sql: TransactionSQL) => T & (T extends PromiseLike<unknown> ? never : unknown)): T {
    return this.#storage.transactionSync(() => {
      let active = true;
      const sql: TransactionSQL = {
        exec: <Row extends Record<string, SqlStorageValue>>(query: string, ...bindings: SqlStorageValue[]) => {
          if (!active) throw new TypeError("Hub transaction has ended");
          // Finish the statement here so a live SQL cursor cannot escape the callback.
          return this.#storage.sql.exec<Row>(query, ...bindings).toArray();
        },
      };
      let result: T;
      try {
        result = fn(sql);
      } finally {
        active = false;
      }
      if (result !== null && (typeof result === "object" || typeof result === "function")
          && typeof Reflect.get(result, "then") === "function") {
        // The caller gets the synchronous error. Consume a native Promise's rejection.
        if (result instanceof Promise) void result.catch(() => {});
        throw new TypeError("Hub transactions must be synchronous");
      }
      return result;
    });
  }
}

interface Migration {
  version: number;
  sql: string;
}

const genesisHash = "0".repeat(64);

// Append each migration with a higher version. Old objects keep their existing rows.
export const schemaMigrations: readonly Migration[] = [
  {
    version: 1,
    sql: `
      CREATE TABLE team (
        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
        workspace_id TEXT NOT NULL,
        policy_kid TEXT NOT NULL,
        audit_kid TEXT NOT NULL,
        audit_seq INTEGER NOT NULL DEFAULT 0,
        audit_tip TEXT NOT NULL DEFAULT '${genesisHash}'
      ) STRICT;

      CREATE TABLE signing_keys (
        kid TEXT PRIMARY KEY,
        purpose TEXT NOT NULL CHECK (purpose IN ('policy', 'audit')),
        public_jwk TEXT NOT NULL,
        status TEXT NOT NULL CHECK (status IN ('staged', 'active', 'retired')),
        wrapped_pkcs8 BLOB,
        nonce BLOB,
        wrap_key_id TEXT
      ) STRICT;

      CREATE TABLE members (
        member_id TEXT PRIMARY KEY,
        role TEXT NOT NULL CHECK (role IN ('lead', 'member')),
        token_hash BLOB NOT NULL UNIQUE CHECK (length(token_hash) = 32)
      ) STRICT;

      CREATE TABLE projects (
        project_id TEXT PRIMARY KEY,
        repo TEXT NOT NULL,
        room_seq INTEGER NOT NULL DEFAULT 0
      ) STRICT;
      CREATE INDEX projects_repo ON projects (repo);

      CREATE TABLE policies (
        project_id TEXT PRIMARY KEY,
        version INTEGER NOT NULL,
        bundle_json TEXT NOT NULL,
        kid TEXT NOT NULL,
        jws_header TEXT NOT NULL,
        jws_signature TEXT NOT NULL
      ) STRICT;

      CREATE TABLE room_events (
        room_id TEXT NOT NULL,
        seq INTEGER NOT NULL,
        event_id TEXT NOT NULL UNIQUE,
        event_json TEXT NOT NULL,
        idempotency_member_id TEXT,
        idempotency_key TEXT,
        PRIMARY KEY (room_id, seq)
      ) STRICT;
      CREATE UNIQUE INDEX room_events_idempotency
        ON room_events (room_id, idempotency_member_id, idempotency_key);

      CREATE TABLE claims (
        claim_id TEXT PRIMARY KEY,
        room_id TEXT NOT NULL,
        member_id TEXT NOT NULL,
        agent_id TEXT NOT NULL,
        agent_vendor TEXT NOT NULL,
        agent_parent TEXT,
        paths_json TEXT NOT NULL,
        intent TEXT NOT NULL,
        mode TEXT NOT NULL,
        expires_at_ms INTEGER NOT NULL
      ) STRICT;
      CREATE INDEX claims_room ON claims (room_id);
      CREATE INDEX claims_expiry ON claims (expires_at_ms);

      CREATE TABLE audit_records (
        seq INTEGER PRIMARY KEY,
        workspace_id TEXT NOT NULL,
        actor_member_id TEXT,
        agent_session_id TEXT,
        reason TEXT,
        action TEXT NOT NULL,
        target TEXT NOT NULL,
        meta_json TEXT NOT NULL,
        flagged INTEGER NOT NULL CHECK (flagged IN (0, 1)),
        created_at TEXT NOT NULL,
        prev_hash TEXT NOT NULL,
        hash TEXT NOT NULL
      ) STRICT;
    `,
  },
  {
    version: 2,
    sql: `
      ALTER TABLE members ADD COLUMN enrollment_id TEXT NOT NULL DEFAULT '';
      UPDATE members SET enrollment_id = lower(hex(randomblob(16)));
      CREATE TABLE invites (
        id TEXT PRIMARY KEY,
        project_id TEXT NOT NULL,
        member_id TEXT NOT NULL,
        hub_role TEXT NOT NULL CHECK (hub_role IN ('member', 'lead')),
        require_approval INTEGER NOT NULL CHECK (require_approval IN (0, 1)),
        state TEXT NOT NULL CHECK (state IN ('issued', 'pending_approval', 'approved', 'redeemed', 'revoked')),
        created_at_ms INTEGER NOT NULL,
        expires_at_ms INTEGER NOT NULL,
        code_hash BLOB NOT NULL UNIQUE CHECK (length(code_hash) = 32),
        claim_hash BLOB CHECK (length(claim_hash) = 32),
        ended_at_ms INTEGER,
        enrollment_id TEXT,
        repo TEXT
      ) STRICT;
      CREATE INDEX invites_live_member ON invites (member_id, expires_at_ms) WHERE ended_at_ms IS NULL;
      CREATE INDEX invites_retention ON invites (COALESCE(ended_at_ms, expires_at_ms));
    `,
  },
  {
    version: 3,
    sql: `
      ALTER TABLE team ADD COLUMN redeem_window_end_ms INTEGER;
      ALTER TABLE team ADD COLUMN redeem_failure_count INTEGER NOT NULL DEFAULT 0;
    `,
  },
  {
    version: 4,
    sql: `
      CREATE TABLE member_observations (
        member_id TEXT NOT NULL,
        project_id TEXT NOT NULL,
        policy_fetch_ms INTEGER,
        policy_version INTEGER,
        room_connected_ms INTEGER,
        PRIMARY KEY (member_id, project_id),
        CHECK ((policy_fetch_ms IS NULL) = (policy_version IS NULL))
      ) STRICT;
    `,
  },
];

export function migrate(storage: HubStorage, migrations = schemaMigrations): number {
  return storage.transaction((sql) => {
    sql.exec("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY) STRICT");
    const [current] = sql.exec<{ version: number }>(
      "SELECT COALESCE(MAX(version), 0) AS version FROM schema_migrations",
    );
    let { version } = current;

    for (const migration of migrations) {
      if (migration.version > version) {
        sql.exec(migration.sql);
        sql.exec("INSERT INTO schema_migrations (version) VALUES (?)", migration.version);
        version = migration.version;
      }
    }
    return version;
  });
}
