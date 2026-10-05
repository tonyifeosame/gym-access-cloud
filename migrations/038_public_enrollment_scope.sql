-- 038: enrollments:write joins the closed scope set.
--
-- The public API gains terminal listing (terminals:read, already in the set) and
-- remote fingerprint enrolment: start at a named terminal, read, cancel. Starting
-- and cancelling commands hardware -- the terminal stops checking fingers at its
-- door until the enrolment ends -- so it is its own scope rather than a use of
-- members:write.
--
-- Numbered 038, not 037: 037 is taken by the OAuth branch (037_datavase_oauth.sql),
-- which ALSO rebuilds this constraint. Whichever of the two lands second must
-- carry both additions (sites:write from 037, enrollments:write from here), or
-- re-adding the constraint will fail against credentials that already hold the
-- other branch's scope.
ALTER TABLE api_credentials DROP CONSTRAINT IF EXISTS api_credentials_scopes_check;
ALTER TABLE api_credentials ADD CONSTRAINT api_credentials_scopes_check CHECK (
    scopes <@ ARRAY[
        'members:read',
        'members:write',
        'sites:read',
        'terminals:read',
        'events:read',
        'access:read',
        'webhooks:manage',
        'enrollments:write'
    ]::text[]
    AND array_length(scopes, 1) >= 1
);
