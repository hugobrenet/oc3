-- 2026-11-05: the resources and the skills of the AI assistant. The MCP
-- servers read them for the agent through GET /ai/resources and /ai/skills,
-- an AIManager writes them. A resource is a reference text the model reads
-- on demand; a skill, one SKILL.md of the Agent Skills specification, its
-- frontmatter kept as JSON. Each is for one or both backends of the agent,
-- om3 and oc3, and is read by everybody, or only by the groups it is
-- published to when it has any.
CREATE TABLE IF NOT EXISTS ai_resources (
  id INT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  title VARCHAR(128) NOT NULL DEFAULT '',
  description VARCHAR(1024) NOT NULL,
  mime_type VARCHAR(32) NOT NULL DEFAULT 'text/markdown',
  backends VARCHAR(16) NOT NULL,
  body MEDIUMTEXT NOT NULL,
  size INT NOT NULL,
  digest VARCHAR(71) NOT NULL,
  updated DATETIME NOT NULL,
  updated_by VARCHAR(255) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_ai_resources_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_resources_publications (
  resource_id INT NOT NULL,
  group_id INT NOT NULL,
  PRIMARY KEY (resource_id, group_id),
  KEY k_ai_resources_publications_group (group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_skills (
  id INT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  backends VARCHAR(16) NOT NULL,
  frontmatter TEXT NOT NULL,
  body MEDIUMTEXT NOT NULL,
  size INT NOT NULL,
  digest VARCHAR(71) NOT NULL,
  updated DATETIME NOT NULL,
  updated_by VARCHAR(255) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_ai_skills_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS ai_skills_publications (
  skill_id INT NOT NULL,
  group_id INT NOT NULL,
  PRIMARY KEY (skill_id, group_id),
  KEY k_ai_skills_publications_group (group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- The privilege to write them, besides Manager.
INSERT INTO auth_group (role, description, privilege)
  SELECT 'AIManager', 'Privilege to manage the resources and the skills of the AI assistant', 'T' FROM DUAL
  WHERE NOT EXISTS (SELECT 1 FROM auth_group WHERE role = 'AIManager');
