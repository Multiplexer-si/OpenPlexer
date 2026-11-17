// Validates spec/ with a non-Go validator, the way an implementer in another language would.
// Usage: node spec-ajv.cjs <repo-root> <node_modules containing ajv 8>
const fs = require("fs"), path = require("path");
const [root, mods] = process.argv.slice(2);
const Ajv2020 = require(path.join(mods, "ajv/dist/2020")).default;
const ajv = new Ajv2020({ strict: false, allErrors: true, validateFormats: false });
const walk = (d) => fs.readdirSync(d, { withFileTypes: true }).flatMap((e) => e.isDirectory() ? walk(path.join(d, e.name)) : [path.join(d, e.name)]);
const files = walk(path.join(root, "spec")).filter((f) => f.endsWith(".json"));
const schemas = files.filter((f) => f.endsWith(".schema.json"));
for (const f of schemas) ajv.addSchema(JSON.parse(fs.readFileSync(f, "utf8")));
let bad = 0, examples = 0, fixtures = 0;
for (const f of files) {
  const m = f.match(/spec\/([^/]+)\/v1\/examples\/([^/]+)\//);
  const isFixture = /\/v1\/fixtures\//.test(f);
  if (!m && !isFixture) continue;
  const id = m ? `urn:openplexer:spec:${m[1]}:v1:${m[2]}` : "urn:openplexer:spec:fixture-file";
  const ok = ajv.validate(id, JSON.parse(fs.readFileSync(f, "utf8")));
  m ? examples++ : fixtures++;
  if (!ok) { bad++; console.log("INVALID", path.relative(root, f), ajv.errorsText()); }
}
console.log(`schemas=${schemas.length} examples=${examples} fixtures=${fixtures} invalid=${bad}`);
process.exit(bad ? 1 : 0);
