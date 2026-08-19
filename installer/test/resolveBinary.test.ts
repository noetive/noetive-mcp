import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";

import { binaryName, defaultFallbackDir, hostKey, packageRoot, platformPackage, resolveBinary } from "../src/resolveBinary";

// postinstall downloads into the package's own bin directory. If resolution
// looks anywhere else, a machine where the download succeeded still reports
// "could not find the binary" — which is exactly what happened before this was
// pinned down.
test("the fallback directory is the same bin directory postinstall writes to", () => {
  assert.equal(defaultFallbackDir(), join(packageRoot(), "bin"));
  assert.ok(existsSync(join(packageRoot(), "package.json")), "packageRoot does not point at the package");
});

// The optional dependency is the path that works under every package manager,
// including pnpm v10, which blocks install scripts by default. It has to be
// tried before the downloaded fallback.
test("the platform package is preferred over the downloaded fallback", () => {
  const resolved = resolveBinary({
    platform: "darwin",
    arch: "arm64",
    resolvePackage: (name) => (name === "@noetive/mcp-server-darwin-arm64" ? "/pkg" : undefined),
    fileExists: () => true,
    fallbackDir: "/fallback",
  });

  assert.equal(resolved, join("/pkg", "bin", "noetive-mcp"));
});

// When install scripts ran but the optional dependency did not land, the
// downloaded binary is what makes the install usable.
test("the downloaded fallback is used when no platform package is installed", () => {
  const resolved = resolveBinary({
    platform: "linux",
    arch: "x64",
    resolvePackage: () => undefined,
    fileExists: (p) => p.startsWith("/fallback"),
    fallbackDir: "/fallback",
  });

  assert.equal(resolved, join("/fallback", "noetive-mcp"));
});

// A missing binary is the failure a user is most likely to hit, and the least
// self-explanatory. The error has to name both places that were searched and
// what to run next.
test("a missing binary names both search paths and a remedy", () => {
  assert.throws(
    () =>
      resolveBinary({
        platform: "linux",
        arch: "x64",
        resolvePackage: () => undefined,
        fileExists: () => false,
        fallbackDir: "/fallback",
      }),
    (err: Error) => {
      assert.match(err.message, /@noetive\/mcp-server-linux-x64/);
      assert.match(err.message, /\/fallback/);
      assert.match(err.message, /npm rebuild/);
      return true;
    },
  );
});

// Windows needs the .exe suffix or the spawn fails with ENOENT.
test("the windows binary carries its extension", () => {
  assert.equal(binaryName("win32"), "noetive-mcp.exe");
  assert.equal(binaryName("darwin"), "noetive-mcp");
});

// An unsupported host should say so, and point at the source build rather than
// leaving the user with nothing.
test("an unsupported platform is named with an alternative", () => {
  assert.throws(() => platformPackage(hostKey("aix", "ppc64")), /go install/);
});

// Each published platform package must be reachable by the key computed from
// process.platform and process.arch, or the wrapper looks for a package that
// was never published.
test("every supported host maps to a package", () => {
  for (const [platform, arch] of [
    ["darwin", "arm64"],
    ["darwin", "x64"],
    ["linux", "arm64"],
    ["linux", "x64"],
    ["win32", "x64"],
  ] as const) {
    assert.ok(platformPackage(hostKey(platform, arch)).startsWith("@noetive/mcp-server-"));
  }
});
