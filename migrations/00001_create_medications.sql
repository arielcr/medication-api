-- +goose Up
CREATE TABLE medications (
    id          uuid        PRIMARY KEY,
    name        text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    dosage      text        NOT NULL CHECK (char_length(dosage) BETWEEN 1 AND 100),
    form        text        NOT NULL CHECK (form IN ('tablet', 'capsule', 'liquid', 'injection', 'cream', 'inhaler')),
    version     bigint      NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);

-- Business rule: the same medication (name + dosage + form, ignoring
-- case) can only be registered once. Enforcing it with a unique index
-- makes it safe under concurrent requests, which an application-level
-- "check then insert" is not.
CREATE UNIQUE INDEX medications_name_dosage_form_key
    ON medications (lower(name), lower(dosage), form);

-- +goose Down
DROP TABLE medications;
