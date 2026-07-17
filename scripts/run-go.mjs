import { spawnSync } from "node:child_process";
import path from "node:path";
import process from "node:process";

const result = spawnSync("go", process.argv.slice(2), {
  cwd: path.resolve("services/qflag"),
  env: {
    ...process.env,
    GOCACHE: path.resolve(".tmp-gocache"),
  },
  stdio: "inherit",
  shell: process.platform === "win32",
});

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}

process.exit(result.status ?? 1);
