import * as common from "./messages/common";
import * as shell from "./messages/shell";
import * as login from "./messages/login";
import * as security from "./messages/security";
import * as system from "./messages/system";
import * as audit from "./messages/audit";
import * as overview from "./messages/overview";
import * as analytics from "./messages/analytics";
import * as jobs from "./messages/jobs";
import * as assets from "./messages/assets";
import * as playground from "./messages/playground";
import * as storage from "./messages/storage";
import * as projects from "./messages/projects";
import * as apikeys from "./messages/apikeys";
import * as cloud from "./messages/cloud";
import * as modes from "./messages/modes";
import * as presets from "./messages/presets";
import * as settings from "./messages/settings";
import * as signing from "./messages/signing";
import * as webhooks from "./messages/webhooks";

/** Per-feature message modules; keys in each must start with "<name>.". */
export const namespaces = {
  common: { en: common.en, uk: common.uk, es: common.es },
  shell: { en: shell.en, uk: shell.uk, es: shell.es },
  login: { en: login.en, uk: login.uk, es: login.es },
  security: { en: security.en, uk: security.uk, es: security.es },
  system: { en: system.en, uk: system.uk, es: system.es },
  audit: { en: audit.en, uk: audit.uk, es: audit.es },
  overview: { en: overview.en, uk: overview.uk, es: overview.es },
  analytics: { en: analytics.en, uk: analytics.uk, es: analytics.es },
  jobs: { en: jobs.en, uk: jobs.uk, es: jobs.es },
  assets: { en: assets.en, uk: assets.uk, es: assets.es },
  playground: { en: playground.en, uk: playground.uk, es: playground.es },
  storage: { en: storage.en, uk: storage.uk, es: storage.es },
  projects: { en: projects.en, uk: projects.uk, es: projects.es },
  apikeys: { en: apikeys.en, uk: apikeys.uk, es: apikeys.es },
  cloud: { en: cloud.en, uk: cloud.uk, es: cloud.es },
  modes: { en: modes.en, uk: modes.uk, es: modes.es },
  presets: { en: presets.en, uk: presets.uk, es: presets.es },
  settings: { en: settings.en, uk: settings.uk, es: settings.es },
  signing: { en: signing.en, uk: signing.uk, es: signing.es },
  webhooks: { en: webhooks.en, uk: webhooks.uk, es: webhooks.es },
};
