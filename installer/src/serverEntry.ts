import { ClientSpec, PACKAGE_NAME } from "./clients";

/** API_KEY_ENV is the one variable the server needs to authenticate. */
export const API_KEY_ENV = "NOETIVE_KEY_SECRET";

/** Where a user obtains the key. Named in output, never assumed known. */
export const DASHBOARD_URL = "https://noetive.io/dashboard";

/** Routing variables the server reads when a tool call does not name them. */
export const TARGETING_ENV = {
  namespace: "NOETIVE_NAMESPACE",
  model: "NOETIVE_MODEL",
  dimensions: "NOETIVE_DIMENSIONS",
} as const;

export interface Targeting {
  readonly namespace?: string;
  readonly model?: string;
  readonly dimensions?: string;
}

export interface EntryOptions {
  /**
   * A literal API key to embed in the config file. Omitted by default: the
   * entry then references the variable instead, so the secret stays out of a
   * dotfile that gets synced, committed or shared in a screen share.
   */
  readonly apiKey?: string;
  readonly targeting?: Targeting;
}

export interface ServerEntry {
  readonly command: string;
  readonly args: string[];
  readonly env?: Record<string, string>;
  readonly [key: string]: unknown;
}

/**
 * entryEnv is the environment the server needs, whatever shape the client
 * stores it in.
 *
 * It is separate from buildEntry because two install strategies need the same
 * answer: an editor configured by file merge gets these as an `env` object,
 * and an editor configured through its own CLI gets them as repeated `--env`
 * flags. Deriving them twice is how the CLI path came to silently drop the API
 * key and the targeting triple while reporting that it had written them.
 *
 *     entryEnv(cursorSpec, { targeting: { namespace: "global" } })
 */
export function entryEnv(spec: ClientSpec, options: EntryOptions = {}): Record<string, string> {
  const env: Record<string, string> = {};

  if (options.apiKey) {
    env[API_KEY_ENV] = options.apiKey;
  } else if (spec.expandsVariables) {
    // A passthrough, not a secret: the editor resolves it from the environment
    // at launch, so the key is never written to disk by this installer.
    env[API_KEY_ENV] = `\${${API_KEY_ENV}}`;
  }

  const targeting = options.targeting ?? {};
  if (targeting.namespace) env[TARGETING_ENV.namespace] = targeting.namespace;
  if (targeting.model) env[TARGETING_ENV.model] = targeting.model;
  if (targeting.dimensions) env[TARGETING_ENV.dimensions] = targeting.dimensions;

  return env;
}

/**
 * buildEntry produces the server entry written under the `noetive` key.
 *
 * `npx -y` is deliberate: an editor spawns the server with no terminal
 * attached, and without -y npx blocks on an install prompt nobody can answer,
 * which surfaces to the user as an editor that never connects.
 *
 *     buildEntry(cursorSpec, { targeting: { namespace: "global" } })
 */
export function buildEntry(spec: ClientSpec, options: EntryOptions = {}): ServerEntry {
  const env = entryEnv(spec, options);

  const entry: Record<string, unknown> = {
    command: "npx",
    args: ["-y", PACKAGE_NAME],
    ...(spec.entryExtras ?? {}),
  };
  if (Object.keys(env).length > 0) entry.env = env;

  return entry as unknown as ServerEntry;
}

/**
 * describeKeyHandling explains where the API key will come from, so the user
 * finds out at install time rather than when the first tool call fails.
 *
 * clientId is passed rather than derived from the spec because the advice ends
 * in a command the user is meant to run, and naming the wrong editor in it
 * sends them to configure something they were not installing.
 */
export function describeKeyHandling(spec: ClientSpec, clientId: string, options: EntryOptions = {}): string {
  if (options.apiKey) {
    return `Your API key was written into the config file. Keep that file out of version control.`;
  }

  // Naming the dashboard matters: nothing else in the install flow tells a
  // first-time user that a key exists or where to get one, and an editor with
  // no key connects successfully and then refuses every call.
  const getKey = `Get an API key from ${DASHBOARD_URL}.`;

  if (spec.expandsVariables) {
    return [
      getKey,
      `The config references \${${API_KEY_ENV}} rather than storing it, so export it where ${spec.displayName} can see it:`,
      ``,
      `    export ${API_KEY_ENV}=keyu_...`,
      ``,
      `Launching ${spec.displayName} from a desktop icon will not pick that up — desktop launchers do not read your shell profile.`,
      `Start it from that same terminal, or re-run this command with --api-key to write the key into the config instead.`,
    ].join("\n");
  }

  return [
    getKey,
    `${spec.displayName} does not expand \${${API_KEY_ENV}} in its config, so no key was written. Re-run with the key to finish:`,
    ``,
    `    npx ${PACKAGE_NAME} init --client ${clientId} --api-key keyu_...`,
  ].join("\n");
}
