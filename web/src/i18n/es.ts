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
import * as agents from "./messages/agents";
import * as moderation from "./messages/moderation";

export const es = {
  ...common.es,
  ...shell.es,
  ...login.es,
  ...security.es,
  ...system.es,
  ...audit.es,
  ...overview.es,
  ...analytics.es,
  ...jobs.es,
  ...assets.es,
  ...playground.es,
  ...storage.es,
  ...projects.es,
  ...apikeys.es,
  ...cloud.es,
  ...modes.es,
  ...presets.es,
  ...settings.es,
  ...signing.es,
  ...webhooks.es,
  ...agents.es,
  ...moderation.es,
};
