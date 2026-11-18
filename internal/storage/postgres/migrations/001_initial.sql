CREATE TABLE study_agent.tasks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL CHECK (owner_id > 0),
    title text NOT NULL CHECK (btrim(title) <> ''),
    description text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    kind text NOT NULL DEFAULT '',
    due_at timestamptz,
    status text NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'done')),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'done') = (completed_at IS NOT NULL)),
    UNIQUE (owner_id, id)
);
CREATE INDEX tasks_owner_due ON study_agent.tasks (owner_id, due_at, id) WHERE status = 'todo';

CREATE TABLE study_agent.lessons (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL CHECK (owner_id > 0),
    source text NOT NULL CHECK (btrim(source) <> ''),
    source_id text NOT NULL CHECK (btrim(source_id) <> ''),
    title text NOT NULL CHECK (btrim(title) <> ''),
    location text NOT NULL DEFAULT '',
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL CHECK (ends_at > starts_at),
    UNIQUE (owner_id, source, source_id),
    UNIQUE (owner_id, id)
);
CREATE INDEX lessons_owner_start ON study_agent.lessons (owner_id, starts_at);

CREATE TABLE study_agent.messages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL CHECK (owner_id > 0),
    chat_id bigint NOT NULL CHECK (chat_id <> 0),
    message_id bigint NOT NULL CHECK (message_id > 0),
    text text NOT NULL,
    sent_at timestamptz NOT NULL,
    UNIQUE (owner_id, chat_id, message_id),
    UNIQUE (owner_id, id)
);

CREATE TABLE study_agent.task_sources (
    owner_id bigint NOT NULL,
    task_id bigint NOT NULL,
    message_id bigint NOT NULL,
    PRIMARY KEY (owner_id, task_id, message_id),
    FOREIGN KEY (owner_id, task_id) REFERENCES study_agent.tasks (owner_id, id) ON DELETE CASCADE,
    FOREIGN KEY (owner_id, message_id) REFERENCES study_agent.messages (owner_id, id) ON DELETE CASCADE
);

CREATE TABLE study_agent.reminders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL CHECK (owner_id > 0),
    task_id bigint,
    lesson_id bigint,
    remind_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'canceled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_attempt_at timestamptz,
    sent_at timestamptz,
    CHECK (num_nonnulls(task_id, lesson_id) = 1),
    CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
    FOREIGN KEY (owner_id, task_id) REFERENCES study_agent.tasks (owner_id, id) ON DELETE CASCADE,
    FOREIGN KEY (owner_id, lesson_id) REFERENCES study_agent.lessons (owner_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX reminders_task_time ON study_agent.reminders (task_id, remind_at) WHERE task_id IS NOT NULL;
CREATE UNIQUE INDEX reminders_lesson_time ON study_agent.reminders (lesson_id, remind_at) WHERE lesson_id IS NOT NULL;
CREATE INDEX reminders_pending ON study_agent.reminders (remind_at, id) WHERE status = 'pending';

CREATE TABLE study_agent.sync_state (
    owner_id bigint NOT NULL CHECK (owner_id > 0),
    source text NOT NULL CHECK (btrim(source) <> ''),
    source_id text NOT NULL CHECK (btrim(source_id) <> ''),
    cursor text NOT NULL,
    synced_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, source, source_id)
);
