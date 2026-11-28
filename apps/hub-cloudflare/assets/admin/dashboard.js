import { renderRecords } from "./records.js";

const element = (id) => document.getElementById(id);
const sections = [...element("sections").querySelectorAll("button[data-view]")];
const picker = element("active-project");
const editor = element("policy-json");
const projects = new Map();
const cursors = new Map();

let session;
let screen = "projects";
let pageRequest = new AbortController();
let pendingLink;
let loadedPolicy;
let publishedVersion;
let roomTimer;
let roomFollow;
let roomCursor = 0;
let roomEvents = [];
let exportURLs = [];

function status(message = "", error = false) {
  const feedback = element("status");
  feedback.textContent = message;
  feedback.toggleAttribute("data-error", error);
}

class RequestError extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

function send(path, options = {}) {
  return fetch("/admin/api" + path, {
    credentials: "same-origin", mode: "same-origin", cache: "no-store", redirect: "error", ...options,
  });
}

async function check(response) {
  if (response.ok) return response;
  const data = await response.json().catch(() => null);
  throw new RequestError(response.status, data?.error ?? "The hub could not complete the request.");
}

async function request(path, { method = "GET", body, signal, raw = false } = {}) {
  const current = session;
  const headers = {};
  if (method !== "GET") headers["X-CSRF-Token"] = current?.csrf ?? "";
  if (body !== undefined) headers["Content-Type"] = "application/json";
  try {
    const response = await check(await send(path, { method, headers, body, signal }));
    return raw ? response : response.status === 204 ? null : await response.json();
  } catch (error) {
    if (current && current === session && error.code === 401) showSignIn();
    throw error;
  }
}

async function act(button, run) {
  if (button) button.disabled = true;
  status();
  try {
    await run();
  } catch (error) {
    if (error.name !== "AbortError") status(error.message, true);
  } finally {
    if (button) button.disabled = false;
  }
}

function action(id, run) {
  const button = element(id);
  button.addEventListener("click", () => void act(button, run));
}

function form(id, run) {
  element(id).addEventListener("submit", (event) => {
    event.preventDefault();
    void act(event.submitter, run);
  });
}

function textButton(label, accessibleName, run, danger = false) {
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = label;
  button.setAttribute("aria-label", accessibleName);
  button.classList.toggle("danger", danger);
  button.addEventListener("click", () => void act(button, run));
  return button;
}

function tableRows(id, records, cells) {
  const table = element(id);
  if (!records.length) {
    const row = document.createElement("tr");
    const cell = row.insertCell();
    cell.colSpan = table.parentElement.tHead.rows[0].cells.length;
    cell.textContent = "No entries on this page.";
    table.replaceChildren(row);
    return;
  }
  table.replaceChildren(...records.map((item) => {
    const row = document.createElement("tr");
    for (const value of cells(item)) {
      const cell = row.insertCell();
      if (Array.isArray(value)) cell.append(...value);
      else cell.textContent = value ?? "—";
    }
    return row;
  }));
}

function setCursor(name, cursor, button = name + "-next") {
  cursors.set(name, cursor);
  element(button).hidden = cursor == null;
}

function setProjects(items) {
  const selected = picker.value;
  for (const project of items) projects.set(project.id, project);
  const options = [...projects.keys()].sort().map((id) => new Option(id, id));
  picker.replaceChildren(...(options.length ? options : [new Option("No projects", "")]));
  picker.disabled = !options.length;
  if (projects.has(selected)) picker.value = selected;
}

function projectPath() {
  return "/projects/" + encodeURIComponent(picker.value) + "/policy";
}

function paged(path, key, cursor, extra = {}) {
  const query = new URLSearchParams({ limit: "25", ...extra });
  if (cursor != null) query.set(key, cursor);
  return path + "?" + query;
}

const time = (value) => value ? value.replace("T", " ").replace(".000Z", " UTC").replace("Z", " UTC") : "—";

async function loadProjects(cursor, signal) {
  const data = await request(paged("/projects", "after", cursor), { signal });
  if (signal.aborted) return;
  setProjects(data.projects);
  setCursor("projects", data.next_cursor);
  tableRows("project-rows", data.projects, ({ id, repo }) => [
    id, repo, [textButton("Select", "Select project " + id, () => {
      picker.value = id;
      status("Active project is " + id + ".");
    })],
  ]);
}

async function loadMembers(cursor, signal) {
  const project = element("filter-members").checked && picker.value ? { project_id: picker.value } : {};
  const data = await request(paged("/members", "after", cursor, project), { signal });
  if (signal.aborted) return;
  setCursor("members", data.next_cursor);
  tableRows("member-rows", data.members, (member) => [
    member.member_id, member.hub_role, member.project_id, member.policy_version,
    time(member.last_policy_fetch_at), time(member.last_room_connected_at),
    [textButton("Revoke", "Revoke member " + member.member_id, async () => {
      if (!confirm("Revoke " + member.member_id + " (" + member.hub_role + ") from the team? Their hub credential will stop working.")) return;
      await request("/members/" + encodeURIComponent(member.member_id), { method: "DELETE" });
      if (session?.member_id === member.member_id) {
        showSignIn();
        status("Your membership was revoked.");
      } else {
        status("Revoked " + member.member_id + ".");
        if (screen === "members") await loadMembers(null, pageRequest.signal);
      }
    }, true)],
  ]);
}

async function loadInvites(cursor, signal) {
  element("invite-project").textContent = picker.value ? "Project " + picker.value : "Add a project to create an invite.";
  element("invite-form").querySelector("button").disabled = !picker.value;
  const data = await request(paged("/invites", "after", cursor), { signal });
  if (signal.aborted) return;
  setCursor("invites", data.next_cursor);
  tableRows("invite-rows", data.invites, (invite) => {
    const actions = [];
    const path = "/invites/" + encodeURIComponent(invite.id);
    if (invite.state === "pending_approval") {
      actions.push(textButton("Approve", "Approve invite for " + invite.member_id, async () => {
        if (!confirm("Has " + invite.member_id + " confirmed public invite ID " + invite.id + " over a trusted channel?")) return;
        await request(path + "/approve", { method: "POST" });
        status("Invite approved for " + invite.member_id + ".");
        if (screen === "invites") await loadInvites(null, pageRequest.signal);
      }));
    }
    if (["issued", "pending_approval", "approved"].includes(invite.state)) {
      actions.push(textButton("Revoke", "Revoke invite for " + invite.member_id, async () => {
        await request(path, { method: "DELETE" });
        status("Invite revoked for " + invite.member_id + ".");
        if (screen === "invites") await loadInvites(null, pageRequest.signal);
      }, true));
    }
    return [invite.member_id, invite.project_id, invite.hub_role, invite.id,
      invite.state.replaceAll("_", " "), time(invite.expires_at), actions];
  });
}

function showPolicyVersion(signed) {
  publishedVersion = signed?.version;
  element("policy-version").textContent = signed
    ? "Published version " + signed.version + " for " + picker.value + "."
    : "No published policy for " + picker.value + ".";
  element("policy-signature-panel").hidden = !signed;
  element("policy-signature").textContent = signed?.signature ?? "";
}

async function loadPolicyHistory(cursor, signal) {
  const data = await request(paged(projectPath() + "/history", "before", cursor), { signal });
  if (signal.aborted) return;
  tableRows("policy-history", data.versions, (row) => [row.version, time(row.created_at), row.actor_member_id]);
  setCursor("history", data.next_cursor, "policy-history-next");
}

async function loadPolicy(signal, reload = false) {
  const project = projects.get(picker.value);
  element("policy-project-content").hidden = !project;
  if (!project) {
    element("policy-version").textContent = "Add a project to edit policy.";
    return;
  }
  if (reload || loadedPolicy !== project.id) {
    element("policy-project-content").hidden = true;
    const signed = await request(projectPath(), { signal }).catch((error) => {
      if (error.code === 404) return null;
      throw error;
    });
    if (signal.aborted) return;
    showPolicyVersion(signed);
    editor.value = JSON.stringify(signed?.bundle ?? {
      version: 1, project_id: project.id, repos: [project.repo], areas: [], roles: {}, members: {},
    }, null, 2);
    element("role-preview").hidden = true;
    loadedPolicy = project.id;
    element("policy-project-content").hidden = false;
  }
  await loadPolicyHistory(null, signal);
}

function previewRoles(preview) {
  const lines = preview.roles.map(({ role, write }) => role + " → " + (write.length ? write.join(", ") : "no write grants"));
  if (!lines.length) lines.push("No roles in this draft.");
  const areas = preview.areas.map(({ name, patterns, system, private: restricted }) =>
    name + (system ? " (system area)" : restricted ? " (private area)" : "") + " → " + patterns.join(", "));
  element("role-rows").replaceChildren(...[lines, areas].filter((group) => group.length).map((group) => {
    const list = document.createElement("ul");
    list.append(...group.map((line) => {
      const item = document.createElement("li");
      item.textContent = line;
      return item;
    }));
    return list;
  }));
  element("role-preview").hidden = false;
}

function stopRoom() {
  roomFollow?.abort();
  roomFollow = undefined;
  clearTimeout(roomTimer);
  element("room-live").setAttribute("aria-pressed", "false");
}

async function loadRoom(after, signal, live = false) {
  const project = picker.value;
  const path = "/rooms/" + encodeURIComponent(project) + "/events";
  element("room-project").textContent = project ? "Project " + project : "Add a project to open the Room.";
  element("room-live").disabled = !project;
  element("room-refresh").disabled = !project;
  if (!project) return;
  const data = await request(paged(path, "last_seq", after), { signal });
  if (signal.aborted) return;
  roomCursor = data.last_seq;
  setCursor("room", data.next_cursor);
  if (live && !data.events.length && roomEvents.length) return;
  roomEvents = live ? [...roomEvents, ...data.events].slice(-50) : data.events;
  element("room-empty").hidden = roomEvents.length > 0;
  renderRecords(element("room-records"), roomEvents,
    (event) => "#" + event.seq + " · " + event.payload.type + " · " + time(event.ts));
}

async function followRoom(signal) {
  if (signal.aborted || element("room-live").getAttribute("aria-pressed") !== "true") return;
  try {
    await loadRoom(roomCursor, signal, true);
    if (!signal.aborted && element("room-live").getAttribute("aria-pressed") === "true") {
      roomTimer = setTimeout(() => void followRoom(signal), 1000);
    }
  } catch (error) {
    if (!signal.aborted) stopRoom();
    if (error.name !== "AbortError") status(error.message, true);
  }
}

async function loadAudit(cursor, signal) {
  const data = await request(paged("/audit", "before", cursor), { signal });
  if (signal.aborted) return;
  setCursor("audit", data.next_cursor);
  element("audit-empty").hidden = data.entries.length > 0;
  renderRecords(element("audit-records"), data.entries,
    (entry) => "#" + entry.id + " · " + entry.action + " · " + time(entry.created_at));
}

const pages = { projects: loadProjects, members: loadMembers, invites: loadInvites, room: loadRoom, audit: loadAudit };

function changeScreen(name) {
  pageRequest.abort();
  pageRequest = new AbortController();
  stopRoom();
  screen = name;
  for (const button of sections) {
    const view = button.dataset.view;
    button.setAttribute("aria-pressed", String(view === name));
    element(view).hidden = view !== name;
  }
  if (name === "room") {
    roomCursor = 0;
    roomEvents = [];
    element("room-records").replaceChildren();
    element("room-empty").hidden = true;
    setCursor("room", null);
  }
  return name === "policy" ? loadPolicy(pageRequest.signal) : pages[name](null, pageRequest.signal);
}

function clearDownloads() {
  for (const url of exportURLs) URL.revokeObjectURL(url);
  exportURLs = [];
  element("audit-downloads").hidden = true;
  element("download-export").removeAttribute("href");
  element("download-checkpoint").removeAttribute("href");
}

function showSignIn() {
  session = undefined;
  pendingLink = undefined;
  loadedPolicy = undefined;
  pageRequest.abort();
  stopRoom();
  clearDownloads();
  projects.clear();
  setProjects([]);
  editor.value = "";
  for (const id of ["project-rows", "member-rows", "invite-rows", "role-rows", "policy-signature", "policy-history",
    "room-records", "audit-records"]) element(id).replaceChildren();
  element("lead-token").value = "";
  element("copy-invite-panel").hidden = true;
  element("workspace").hidden = true;
  element("identity").hidden = true;
  element("signed-in-member").textContent = "";
  element("sign-in-panel").hidden = false;
}

async function enter(active) {
  session = active;
  element("sign-in-panel").hidden = true;
  element("workspace").hidden = false;
  element("signed-in-member").textContent = active.member_id;
  element("identity").hidden = false;
  await changeScreen("projects");
}

form("sign-in-form", async () => {
  const input = element("lead-token");
  let token = input.value.trim();
  input.value = "";
  let signed;
  try {
    signed = send("/sign-in", {
      method: "POST", body: "{}", headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
    });
  } finally {
    token = "";
  }
  const response = await check(await signed);
  await enter(await response.json());
});

action("sign-out", async () => {
  await request("/sign-out", { method: "POST" });
  showSignIn();
  status("Signed out.");
});

for (const button of sections) button.addEventListener("click", () => void act(null, () => changeScreen(button.dataset.view)));
picker.addEventListener("change", () => void act(null, () => changeScreen(screen)));
element("filter-members").addEventListener("change", () => void act(null, () => changeScreen("members")));

for (const [name, load] of Object.entries(pages)) {
  action(name + "-refresh", () => changeScreen(name));
  action(name + "-next", () => {
    if (name === "room") stopRoom();
    return load(cursors.get(name), pageRequest.signal);
  });
}

form("project-form", async () => {
  const id = element("project-id").value;
  const repo = element("project-repo").value;
  await request("/projects", { method: "POST", body: JSON.stringify({ id, repo }) });
  element("project-form").reset();
  if (!session) return;
  setProjects([{ id, repo }]);
  picker.value = id;
  status("Added project " + id + ".");
  if (screen === "projects") await loadProjects(null, pageRequest.signal);
});

form("invite-form", async () => {
  const project_id = picker.value;
  const member_id = element("invite-member").value;
  const hub_role = element("invite-role").value;
  const expires_in_seconds = element("invite-expiry").valueAsNumber * 60;
  const require_approval = element("invite-approval").checked;
  if (pendingLink && !confirm("Create another invite and discard the join link you have not copied?")) return;
  if (hub_role === "lead" && !confirm("Issue an invite for " + member_id + " to project " + project_id +
      " with the Lead role? Leads have team-wide admin access.")) return;
  const current = session;
  const data = await request("/invites", {
    method: "POST", body: JSON.stringify({ project_id, member_id, hub_role, expires_in_seconds, require_approval }),
  });
  if (session !== current) return;
  pendingLink = data.link;
  element("invite-form").reset();
  element("copy-invite-panel").hidden = false;
  status("Created invite for " + member_id + ".");
  if (screen === "invites") await loadInvites(null, pageRequest.signal);
});

action("copy-invite", async () => {
  if (!pendingLink) return;
  const link = pendingLink;
  await navigator.clipboard.writeText(link);
  if (link === pendingLink) {
    pendingLink = undefined;
    element("copy-invite-panel").hidden = true;
  }
  status("Join link copied.");
});

action("next-version", () => {
  let draft;
  try {
    draft = JSON.parse(editor.value);
    if (!draft || typeof draft !== "object" || Array.isArray(draft)) throw new Error();
  } catch {
    throw new Error("The policy draft must be a JSON object.");
  }
  const next = (publishedVersion ?? 0) + 1;
  if (!Number.isSafeInteger(next)) throw new Error("The published version is already at the policy limit.");
  draft.version = next;
  editor.value = JSON.stringify(draft, null, 2);
  element("role-preview").hidden = true;
  status("Prepared version " + next + ".");
});

editor.addEventListener("input", () => { element("role-preview").hidden = true; });

action("preview-policy", async () => {
  const path = projectPath();
  const draft = editor.value;
  const signal = pageRequest.signal;
  const preview = await request(path + "/preview", { method: "POST", body: draft, signal });
  if (signal.aborted || editor.value !== draft) return;
  previewRoles(preview);
  status("Policy draft is valid.");
});

form("policy-form", async () => {
  const path = projectPath();
  const draft = editor.value;
  const current = session;
  const preview = await request(path + "/preview", { method: "POST", body: draft });
  if (session !== current) return;
  const signed = await request(path, { method: "PUT", body: draft });
  status("Published policy version " + signed.version + " for " + signed.bundle.project_id + ".");
  if (session === current && loadedPolicy === signed.bundle.project_id) {
    if (picker.value === signed.bundle.project_id) showPolicyVersion(signed);
    else loadedPolicy = undefined;
    if (draft === editor.value) previewRoles(preview);
    if (screen === "policy" && picker.value === signed.bundle.project_id) await loadPolicyHistory(null, pageRequest.signal);
  }
});

action("reload-policy", () => loadPolicy(pageRequest.signal, true));
action("policy-history-next", () => loadPolicyHistory(cursors.get("history"), pageRequest.signal));

element("room-live").addEventListener("click", () => {
  if (element("room-live").getAttribute("aria-pressed") === "true") {
    stopRoom();
  } else {
    roomFollow = new AbortController();
    element("room-live").setAttribute("aria-pressed", "true");
    void followRoom(roomFollow.signal);
  }
});

action("export-audit", async () => {
  const current = session;
  const response = await request("/audit/export", { raw: true });
  const checkpoint = response.headers.get("OpenPlexer-Audit-Checkpoint");
  if (!checkpoint) throw new Error("The audit export did not include a signed checkpoint.");
  const entries = await response.blob();
  if (session !== current) return;
  clearDownloads();
  exportURLs = [entries, new Blob([checkpoint], { type: "text/plain" })].map((data) => URL.createObjectURL(data));
  element("download-export").href = exportURLs[0];
  element("download-checkpoint").href = exportURLs[1];
  element("audit-downloads").hidden = false;
  status("Download both files to verify the export.");
});

request("/session").then(enter).catch((error) => {
  showSignIn();
  if (error.code !== 401) status(error.message, true);
});
