import type { Choice, Prompter } from "./prompt";
import { API_KEY_ENV, DASHBOARD_URL, POLICY_ENV, TARGETING_ENV } from "./serverEntry";

/**
 * The testing defaults: what the shared namespace is provisioned with, so an
 * install nobody answered questions for works on the first call.
 *
 * Applied only with the user told about it. The server still refuses to invent
 * a namespace; the installer writes one into the config where it can be seen,
 * and says on every such install that the namespace is shared.
 */
export const DefaultNamespace = "global";
export const SuggestedModel = "Qwen3-Embedding-4B";
export const SuggestedDimensions = "1024";

/**
 * The questions, worded once, so the prompts and the summary of what was
 * chosen without asking describe the same settings.
 */
const NamespaceQuestion = `Namespace for calls that do not name one, for example "acme-platform"`;
const ModelQuestion = `Embedding model that namespace is provisioned with`;
const DimensionsQuestion = `Dimensions for that model`;
const SharedNamespaceQuestion = `Close the shared "global" namespace? It spans tenants, so agents can read and write what other Noetive users publish there`;
const SkillsQuestion = `Skills to install. These teach the agent how to write queries and when to reach for Noetive`;

/**
 * Defaulted is one setting nobody answered and `init` filled in, with what the
 * user needs to know about it and the flag that would change it.
 */
export interface Defaulted {
  readonly setting: string;
  readonly value: string;
  /** Set on a default the user should not leave in place unknowingly. */
  readonly warning?: string;
  /** Flags that replace this default on a rerun. */
  readonly change: string;
}

/**
 * isShared reports whether a namespace is the cross-tenant one. Names are
 * case-insensitive, so `Global` is the same namespace.
 */
export function isShared(namespace: string | undefined): boolean {
  return namespace?.toLowerCase() === DefaultNamespace;
}

/**
 * withDefaults fills every setting the flags left open with what pressing Enter
 * through the interview would choose, and lists what it filled in.
 *
 * The two must agree: an install from an agent's shell and one from a person
 * who accepted every suggestion should be the same install.
 *
 *	withDefaults({ expandsVariables: true, offeredSkills: [] })
 *	// => { answers: { namespace: "global", model: "Qwen3-Embedding-4B", ... },
 *	//      defaulted: [{ setting: "namespace", value: "global", warning: "..." }, ...] }
 */
export function withDefaults(given: Given): { answers: Answers; defaulted: Defaulted[] } {
  const defaulted: Defaulted[] = [];

  const namespace = given.namespace ?? DefaultNamespace;
  if (given.namespace === undefined) {
    defaulted.push({
      setting: "namespace",
      value: DefaultNamespace,
      warning:
        `"${DefaultNamespace}" is shared by every Noetive user. What your agents publish there, others can find. ` +
        `Fine for trying it out; not for your organisation's work.`,
      change: "--namespace <name> --disable-global-ns",
    });
  }

  // Model and dimensions describe a namespace, so an empty one gets neither.
  const model = given.model ?? (namespace ? SuggestedModel : undefined);
  if (given.model === undefined && model) {
    defaulted.push({ setting: "model", value: model, change: "--model <name>" });
  }
  const dimensions = given.dimensions ?? (namespace ? SuggestedDimensions : undefined);
  if (given.dimensions === undefined && dimensions) {
    defaulted.push({ setting: "dimensions", value: dimensions, change: "--dimensions <n>" });
  }

  // Open while the shared namespace is the one in use, since closing it would
  // refuse every call. Closed once the user names their own, which is the
  // interview's suggestion: there is then nothing to gain from the shared one
  // and an agent unsure where to publish will reach for it.
  const disableGlobalNamespace = given.disableGlobalNamespace ?? !isShared(namespace);
  if (given.disableGlobalNamespace === undefined) {
    defaulted.push({
      setting: "shared namespace",
      value: disableGlobalNamespace ? "closed" : "open",
      change: disableGlobalNamespace ? "--allow-global-ns" : "--disable-global-ns",
    });
  }

  const skills = given.skills ?? given.offeredSkills.map((skill) => skill.value);
  if (given.skills === undefined && skills.length > 0) {
    defaulted.push({ setting: "skills", value: skills.join(", "), change: "--skills none" });
  }

  return {
    answers: {
      ...(given.apiKey ? { apiKey: given.apiKey } : {}),
      ...(namespace ? { namespace } : {}),
      ...(model ? { model } : {}),
      ...(dimensions ? { dimensions } : {}),
      disableGlobalNamespace,
      skills,
    },
    defaulted,
  };
}


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

  // Not asked while the shared namespace is the one in use: closing it would
  // refuse every call this install routes, so "yes" is never the right answer.
  const disableGlobalNamespace =
    given.disableGlobalNamespace ?? (isShared(namespace) ? false : await askAboutSharedNamespace(prompter));
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
    validate: (value) => keyComplaint(value),
  });

  return key || undefined;
}

/**
 * keyComplaint says what is wrong with a value offered as the API key, or
 * nothing when it could be one.
 *
 * A variable reference is the common mistake: `'$NOETIVE_KEY_SECRET'` quoted so
 * the shell never expanded it, written into a config that will send that text
 * to Noetive and get "unauthorized" back. Empty is not a complaint; it means
 * "write no key".
 *
 *	keyComplaint("${NOETIVE_KEY_SECRET}") // => "That is a variable reference, ..."
 *	keyComplaint("keya_...")              // => undefined
 */
export function keyComplaint(value: string): string | undefined {
  if (value.startsWith("$") || value.startsWith("%")) {
    return `That is a variable reference, not a key. Pass the key itself, or leave it empty and export ${API_KEY_ENV} instead.`;
  }
  return undefined;
}

/**
 * askForNamespace asks where calls go when the agent does not say.
 *
 * The default is the shared namespace, so a first install works without
 * knowing anything yet, and the question says it is shared so pressing Enter
 * is a choice made knowingly. The value is written into the config, where it
 * can be seen and changed; the server still invents nothing.
 */
function askForNamespace(prompter: Prompter): Promise<string> {
  return prompter.text(
    `${NamespaceQuestion}. "${DefaultNamespace}" is shared with every Noetive user, fine for trying it out (${TARGETING_ENV.namespace})`,
    { default: DefaultNamespace },
  );
}

function askForModel(prompter: Prompter): Promise<string> {
  return prompter.text(`${ModelQuestion} (${TARGETING_ENV.model})`, {
    default: SuggestedModel,
  });
}

function askForDimensions(prompter: Prompter): Promise<string> {
  return prompter.text(`${DimensionsQuestion} (${TARGETING_ENV.dimensions})`, {
    default: SuggestedDimensions,
    validate: dimensionsComplaint,
  });
}

/**
 * dimensionsComplaint says what is wrong with a value offered as the
 * dimensionality, or nothing when it could be one. Empty is not a complaint; it
 * means "write none".
 *
 * Checked by the installer rather than left to the server, for the prompt and
 * the flag alike. A typo in a config file surfaces as a server that will not
 * start, reported into a log the editor collects and nobody reads.
 *
 *	dimensionsComplaint("1024") // => undefined
 *	dimensionsComplaint("abc")  // => "A whole number between 1 and 65535, ..."
 */
export function dimensionsComplaint(value: string): string | undefined {
  if (!value) return undefined;
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    return "A whole number between 1 and 65535, matching the model.";
  }
  return undefined;
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
    `${SharedNamespaceQuestion} (${POLICY_ENV.disableGlobalNamespace})`,
    { default: true },
  );
}

async function askAboutSkills(prompter: Prompter, offered: readonly Choice[]): Promise<string[]> {
  if (offered.length === 0) return [];

  return await prompter.multiSelect(
    SkillsQuestion,
    offered,
    offered.map((choice) => choice.value),
  );
}
