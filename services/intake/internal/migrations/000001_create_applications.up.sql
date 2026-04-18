CREATE TABLE applications (
    id             UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        TEXT         NOT NULL,
    job_title      TEXT         NOT NULL,
    company        TEXT         NOT NULL,
    url            TEXT         NOT NULL,
    source         SMALLINT     NOT NULL DEFAULT 0,
    status         SMALLINT     NOT NULL DEFAULT 1,
    applied_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    schema_version TEXT         NOT NULL DEFAULT '1.0'
);

CREATE INDEX idx_applications_user_id ON applications(user_id);
