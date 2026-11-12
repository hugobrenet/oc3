-- 2026-11-10: how often the AI assistants load each skill, by backend, as
-- ai_resources_reads counts the resources: the reads of its SKILL.md through
-- GET /ai/skills/{name} with a backend. Deleted with the skill.
CREATE TABLE IF NOT EXISTS ai_skills_reads (
  skill_id INT NOT NULL,
  backend VARCHAR(8) NOT NULL,
  read_count INT NOT NULL DEFAULT 0,
  last_read DATETIME NOT NULL,
  PRIMARY KEY (skill_id, backend)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
