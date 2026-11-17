// Usage: node gate-files.cjs <operation> <file> [argument]
// The file operations of gate.sh, which plays the part of a damaged file, of time that passes, and of a log reader.
//   flip <file> <key>    changes one character of the signed payload of the compact JWS at <key>
//   age <file> <hours>   moves confirmed_at of the stored policy <hours> into the past
//   log <file>           checks that each audit record links to the record before it, and prints the actions
const fs = require("fs");
const [operation, file, argument] = process.argv.slice(2);
const text = fs.readFileSync(file, "utf8");

if (operation === "log") {
  const records = text.trimEnd().split("\n").map((line) => JSON.parse(line));
  let previous = "0".repeat(64);
  records.forEach((record, index) => {
    if (record.prev_hash !== previous || record.id !== String(index + 1)) {
      throw new Error(`record ${record.id} does not link to the record before it`);
    }
    previous = record.hash;
  });
  console.log(records.map((record) => record.action).join(" "));
} else {
  const doc = JSON.parse(text);
  if (operation === "flip") {
    const segments = doc[argument].split(".");
    segments[1] = (segments[1][0] === "e" ? "f" : "e") + segments[1].slice(1);
    doc[argument] = segments.join(".");
  } else if (operation === "age") {
    doc.confirmed_at = new Date(Date.now() - Number(argument) * 3600 * 1000).toISOString();
  } else {
    throw new Error(`unknown operation ${operation}`);
  }
  fs.writeFileSync(file, JSON.stringify(doc));
}
