# Contributing

HomeBox is being implemented as a self-hosted Go/SQLite and React PWA. Read [readme.md](readme.md) before contributing.

- Work on a feature branch and commit each completed, tested, independently reviewable task.
- Use English [Conventional Commits](https://www.conventionalcommits.org/), for example `feat(inventory): enforce nonnegative stock transfers`.
- Write documentation, code comments, and docstrings in English. Traditional Chinese documentation is a secondary translation of the completed English originals; the product UI defaults to Traditional Chinese.
- Update executable contracts, clients, tests, and `CHANGELOG.md` together when changing APIs or persistence.
- Public builds and tests must not require private specifications or local development records. Do not commit `dev/`, `CLAUDE.md`, internal progress/decision logs, or specification audit notes.
- Never include real household data, personal photos, passwords, environment files, databases, private backups, or build artifacts in commits.
- Camera, offline, security, financial, and backup features require working implementations, not static screens or fake handlers. Distinguish simulated tests from actual device evidence.
- Report security issues privately to the maintainer. Do not attach credentials or personal data to public issues or publish exploitable details before a fix is available.
