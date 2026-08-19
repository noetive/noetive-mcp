#!/usr/bin/env node
"use strict";

// Assembles one npm package per platform, each carrying a single binary.
//
// This is the esbuild pattern, and it is the primary delivery path rather than
// a download because it is the only one that works everywhere: npm, pnpm and
// yarn each install exactly the package whose os/cpu fields match the host, and
// they do it without running an install script — which pnpm v10 blocks by
// default.
//
//     node scripts/build-platform-packages.js 1.4.0
//
// Reads GoReleaser's build output from dist/ and writes dist/npm/<name>/.

const { chmodSync, copyFileSync, existsSync, mkdirSync, writeFileSync } = require("node:fs");
const { join } = require("node:path");

const ROOT = join(__dirname, "..");
const DIST = join(ROOT, "dist");

// `build` is GoReleaser's per-platform output directory, not the release asset
// name: the asset name only exists in the published release, while this script
// runs against what is actually on disk.
//
// Keep the package names in step with PLATFORM_PACKAGES in
// installer/src/resolveBinary.ts, and the build paths in step with the
// extra_files globs in .goreleaser.yaml.
const TARGETS = [
  { pkg: "@noetive/mcp-server-darwin-arm64", build: "noetive-mcp_darwin_arm64", os: "darwin", cpu: "arm64", bin: "noetive-mcp" },
  { pkg: "@noetive/mcp-server-darwin-x64", build: "noetive-mcp_darwin_amd64_v1", os: "darwin", cpu: "x64", bin: "noetive-mcp" },
  { pkg: "@noetive/mcp-server-linux-arm64", build: "noetive-mcp_linux_arm64", os: "linux", cpu: "arm64", bin: "noetive-mcp" },
  { pkg: "@noetive/mcp-server-linux-x64", build: "noetive-mcp_linux_amd64_v1", os: "linux", cpu: "x64", bin: "noetive-mcp" },
  { pkg: "@noetive/mcp-server-win32-x64", build: "noetive-mcp_windows_amd64_v1", os: "win32", cpu: "x64", bin: "noetive-mcp.exe" },
];

function main() {
  const version = process.argv[2];
  if (!version) {
    console.error("usage: build-platform-packages.js <version>");
    process.exit(2);
  }

  for (const target of TARGETS) {
    const source = join(DIST, target.build, target.bin);
    // A missing binary is fatal. Skipping it would publish a wrapper whose
    // optional dependency resolves to nothing on that platform, and the user
    // would find out at first launch rather than at release time.
    if (!existsSync(source)) {
      console.error(`missing release binary ${source}; the release build did not produce every platform`);
      process.exit(1);
    }

    const dir = join(DIST, "npm", target.pkg.replace("@noetive/", ""));
    mkdirSync(join(dir, "bin"), { recursive: true });

    const binary = join(dir, "bin", target.bin);
    copyFileSync(source, binary);
    chmodSync(binary, 0o755);

    writeFileSync(
      join(dir, "package.json"),
      `${JSON.stringify(
        {
          name: target.pkg,
          version,
          description: `noetive-mcp binary for ${target.os}-${target.cpu}`,
          license: "ISC",
          repository: { type: "git", url: "git+https://github.com/noetive/noetive-mcp.git" },
          // npm reads these to decide whether to install the package at all,
          // which is what keeps four of five binaries off every machine.
          os: [target.os],
          cpu: [target.cpu],
          files: ["bin/"],
        },
        null,
        2,
      )}\n`,
    );

    writeFileSync(
      join(dir, "README.md"),
      `# ${target.pkg}\n\nPlatform binary for [@noetive/mcp-server](https://www.npmjs.com/package/@noetive/mcp-server).\nInstalled automatically on ${target.os}-${target.cpu}; do not depend on it directly.\n`,
    );

    console.log(`built ${target.pkg}@${version}`);
  }
}

main();
