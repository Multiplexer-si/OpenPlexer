import { writeFile } from "node:fs/promises";
import { URL } from "node:url";
import { generateVectors, renderVectors } from "./vector-data.ts";

await writeFile(new URL("vectors/ts-signed.json", import.meta.url), renderVectors(await generateVectors()));
