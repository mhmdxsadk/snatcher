# Versioning

For every change set, increment `Release` in `internal/version/version.go`.
Use a patch increment by default; use a minor or major increment when appropriate
for the scope of the change. Keep release references centralized in this package.
The public API version (`API`) is separate and only changes for API versioning.
