# Contributing to Yearning v4

Thank you for your interest in contributing to Yearning v4.

## Licensing and Contributor License Agreement (CLA)

Yearning v4 is released under the **GNU Affero General Public License v3.0 or later (AGPL-3.0-or-later)** (see [LICENSE](./LICENSE)).

Because Yearning may also be offered under separate commercial licenses, **all external contributors must accept the [Contributor License Agreement (CLA)](./CLA.md)** before a pull request can be merged. Please confirm acceptance using the checkbox in the pull request template.

## Local Verification

Before opening a pull request, please verify both backend and frontend builds and tests locally:

```bash
make backend-build
make backend-test
make frontend-install
make frontend-build
make frontend-test
```

## Pull Request Guidelines

- Keep changes focused and include unit tests covering new behavior or bug fixes.
- Write source code, comments, and test descriptions in English.
- Ensure no secrets, private credentials, or environment-specific endpoints are included.
