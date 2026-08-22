import type { Choice, Prompter } from "./prompt";
import { API_KEY_ENV, DASHBOARD_URL, POLICY_ENV, TARGETING_ENV } from "./serverEntry";

/**
 * SuggestedModel and SuggestedDimensions are what the shared namespace is
 * provisioned with.
 *
 * Offered as a suggestion, never applied on the caller's behalf. The server
 * refuses to invent these, and an installer that filled them in silently would
 * be doing exactly what the server exists to prevent, one layer earlier.
 */
export const SuggestedModel = "Qwen3-Embedding-4B";
export const SuggestedDimensions = "1024";

/** Answers is everything `init` needs that a flag did not already supply. */
export interface Answers {
  readonly apiKey?: string;
  readonly namespace?: string;
  readonly model?: string;
  readonly dimensions?: string;
  readonly disableGlobalNamespace?: boolean;
  readonly skills: readonly string[];
}

/**
 * Given is what the command line already answered.
 *
 * A question whose flag was passed is not asked. Someone who spelled out every
 * flag has already said what they want, and asking again turns a scripted
 * install into an interactive one.
 */
export interface Given {
  readonly apiKey?: string;
  readonly namespace?: string;
  readonly model?: string;
  readonly dimensions?: string;
  readonly disableGlobalNamespace?: boolean;
  readonly skills?: readonly string[];
  /** Whether this editor substitutes ${VAR} in its own config. */
  readonly expandsVariables: boolean;
  /** Skills this editor can be given, empty when it has nowhere to put them. */
  readonly offeredSkills: readonly Choice[];
}

/**
 * interview asks for the settings `init` writes, and returns them alongside
 * whatever the flags already decided.
 *
 * The order matters. It runs from the one setting without which nothing works
 * to the ones a first install can reasonably skip, so someone who stops
 * answering partway through still has a server that authenticates.
 */
export async function interview(prompter: Prompter, given: Given): Promise<Answers> {
  const apiKey = given.apiKey ?? (await askForKey(prompter, given.expandsVariables));
  const namespace = given.namespace ?? (await askForNamespace(prompter));

  // Model and dimensions only mean something once a namespace is named: they
  // describe how that namespace embeds. Asking for them against no namespace
  // collects two values that will never be used together.
  const model = given.model ?? (namespace ? await askForModel(prompter) : undefined);
  const dimensions = given.dimensions ?? (namespace ? await askForDimensions(prompter) : undefined);

  const disableGlobalNamespace = given.disableGlobalNamespace ?? (await askAboutSharedNamespace(prompter));
  const skills = given.skills ?? (await askAboutSkills(prompter, given.offeredSkills));

  return {
    ...(apiKey ? { apiKey } : {}),
    ...(namespace ? { namespace } : {}),
    ...(model ? { model } : {}),
    ...(dimensions ? { dimensions } : {}),
    disableGlobalNamespace,
    skills,
  };
}

/**
 * askForKey offers to reference the variable rather than write the key, which
 * is what keeps the secret out of a file that gets synced or committed.
 *
 * Editors that do not expand variables get no such offer. Writing
 * `${NOETIVE_KEY_SECRET}` into a config that will never substitute it produces
 * a server that reports "unauthorized" and sends the user to check an account
 * that is fine.
 */
async function askForKey(prompter: Prompter, expandsVariables: boolean): Promise<string | undefined> {
  if (expandsVariables) {
    const embed = await prompter.confirm(
      `Write your API key into the config? Answering no references ${API_KEY_ENV} instead, so the key stays out of the file`,
      { default: false },
    );
    if (!embed) return undefined;
  }

  // The reason is carried in the question rather than printed beside it. This
  // runs inside a terminal the prompter owns, and a stray write from here lands
  // in the middle of a redrawn prompt.
  const why = expandsVariables
    ? ""
    : ` (this editor does not expand ${API_KEY_ENV}, so leaving it empty means exporting it before launch)`;

  const key = await prompter.text(`API key from ${DASHBOARD_URL}${why}`, {
    secret: true,
    validate: (value) => {
      if (!value) return undefined;
      if (value.startsWith("$") || value.startsWith("%")) {
        return `That is a variable reference, not a key. Paste the key itself, or leave this empty and export ${API_KEY_ENV} instead.`;
      }
      return undefined;
    },
  });

  return key || undefined;
}

/**
 * askForNamespace asks where calls go when the agent does not say.
 *
 * Deliberately offered with no default. Every other question here can fall back
 * to something sensible; this one cannot, because a namespace nobody chose is
 * the failure the whole product is built to refuse.
 */
function askForNamespace(prompter: Prompter): Promise<string> {
  return prompter.text(
    `Namespace for calls that do not name one, for example "acme-platform". Leave empty to name one on every call (${TARGETING_ENV.namespace})`,
  );
}

function askForModel(prompter: Prompter): Promise<string> {
  return prompter.text(`Embedding model that namespace is provisioned with (${TARGETING_ENV.model})`, {
    default: SuggestedModel,
  });
}

function askForDimensions(prompter: Prompter): Promise<string> {
  return prompter.text(`Dimensions for that model (${TARGETING_ENV.dimensions})`, {
    default: SuggestedDimensions,
    // Checked here rather than left to the server. A typo in a config file
    // surfaces as a server that will not start, reported into a log the editor
    // collects and nobody reads.
    validate: (value) => {
      if (!value) return undefined;
      const parsed = Number(value);
      if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
        return "A whole number between 1 and 65535, matching the model.";
      }
      return undefined;
    },
  });
}

/**
 * askAboutSharedNamespace offers to close `global`.
 *
 * It defaults to closing it. `global` spans tenants and is the example value in
 * every piece of guidance an agent reads, which makes it the namespace an agent
 * reaches for when it is unsure. For anyone whose work is not meant to be
 * shared across tenants, that default is a leak, and the person installing is
 * the only one in a position to say which case they are in.
 */
function askAboutSharedNamespace(prompter: Prompter): Promise<boolean> {
  return prompter.confirm(
    `Close the shared "global" namespace? It spans tenants, so agents can read and write what other Noetive users publish there (${POLICY_ENV.disableGlobalNamespace})`,
    { default: true },
  );
}

async function askAboutSkills(prompter: Prompter, offered: readonly Choice[]): Promise<string[]> {
  if (offered.length === 0) return [];

  return await prompter.multiSelect(
    "Skills to install. These teach the agent how to write queries and when to reach for Noetive",
    offered,
    offered.map((choice) => choice.value),
  );
}
