import { existsSync } from "node:fs";
import { join } from "node:path";

/** PLATFORM_PACKAGES maps a host to the package carrying its binary. */
export const PLATFORM_PACKAGES: Readonly<Record<string, string>> = {
  "darwin-arm64": "@noetive/mcp-server-darwin-arm64",
  "darwin-x64": "@noetive/mcp-server-darwin-x64",
  "linux-arm64": "@noetive/mcp-server-linux-arm64",
  "linux-x64": "@noetive/mcp-server-linux-x64",
  "win32-x64": "@noetive/mcp-server-win32-x64",
};

/**
 * packageRoot is the installed package's own directory.
 *
 * Computed from the compiled module's location rather than from the working
 * directory, because the wrapper runs from wherever the editor happened to
 * launch it. The two levels correspond to dist/src, which is where tsc places
 * this module.
 */
export function packageRoot(): string {
  return join(__dirname, "..", "..");
}

/** hostKey identifies the current platform in PLATFORM_PACKAGES terms. */
export function hostKey(platform: string = process.platform, arch: string = process.arch): string {
  return `${platform}-${arch}`;
}

/** binaryName is the executable inside a platform package. */
export function binaryName(platform: string = process.platform): string {
  return platform === "win32" ? "noetive-mcp.exe" : "noetive-mcp";
}

/** platformPackage names the package for the host, or throws listing support. */
export function platformPackage(key: string = hostKey()): string {
  const name = PLATFORM_PACKAGES[key];
  if (!name) {
    throw new Error(
      `${key} is not a supported platform. Supported: ${Object.keys(PLATFORM_PACKAGES).join(", ")}. ` +
        `You can still build from source: go install github.com/noetive/noetive-mcp/cmd/noetive-mcp@latest`,
    );
  }
  return name;
}

/**
 * defaultFallbackDir is where postinstall places a downloaded binary.
 *
 * It must be the package's own bin directory — the same one postinstall writes
 * to. Pointing anywhere else makes the fallback silently unreachable, and the
 * user sees "could not find the binary" on a machine where it was downloaded
 * successfully.
 */
export function defaultFallbackDir(): string {
  return join(packageRoot(), "bin");
}

export interface ResolveOptions {
  readonly platform?: string;
  readonly arch?: string;
  /** Overridden in tests; resolves a package's directory the way require does. */
  readonly resolvePackage?: (name: string) => string | undefined;
  readonly fileExists?: (path: string) => boolean;
  /** Where postinstall places a downloaded binary when no package is present. */
  readonly fallbackDir?: string;
}

/**
 * resolveBinary finds the platform binary this wrapper should execute.
 *
 * The optional dependency is tried first: npm, pnpm and yarn all install only
 * the package matching the host's os and cpu fields, and that path works even
 * when install scripts are disabled — which pnpm v10 does by default. The
 * download performed by postinstall is the fallback for the cases where the
 * optional dependency did not land.
 *
 * Returns the executable path, or throws with the remediation.
 */
export function resolveBinary(options: ResolveOptions = {}): string {
  const platform = options.platform ?? process.platform;
  const arch = options.arch ?? process.arch;
  const exists = options.fileExists ?? existsSync;
  const resolvePackage = options.resolvePackage ?? defaultResolvePackage;

  const packageName = platformPackage(hostKey(platform, arch));
  const executable = binaryName(platform);

  const packageDir = resolvePackage(packageName);
  if (packageDir) {
    const candidate = join(packageDir, "bin", executable);
    if (exists(candidate)) return candidate;
  }

  const fallback = join(options.fallbackDir ?? defaultFallbackDir(), executable);
  if (exists(fallback)) return fallback;

  throw new Error(
    `could not find the noetive-mcp binary for ${hostKey(platform, arch)}.\n` +
      `Expected it in the ${packageName} package or at ${fallback}.\n` +
      `Reinstall with \`npm install -g @noetive/mcp-server\`, or if your package manager blocks install scripts, ` +
      `run \`npm rebuild @noetive/mcp-server\`.`,
  );
}

function defaultResolvePackage(name: string): string | undefined {
  try {
    // The package.json is resolvable even when the package has no main entry,
    // which a binary-only package does not.
    return join(require.resolve(`${name}/package.json`), "..");
  } catch {
    return undefined;
  }
}
