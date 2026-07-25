# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities through GitHub's private vulnerability reporting for this repository. Do not open a public issue for an undisclosed vulnerability.

Include the affected command, required inputs, impact, and a minimal reproduction when possible. Reports are handled on a best-effort basis.

## Trust model

`skillsrc` verifies source identity, locked revisions, content hashes, and installation ownership. These controls make installations reproducible and prevent `skillsrc` from overwriting unmanaged content. They do not establish that instructions in an upstream skill are trustworthy. Review sources and lockfile changes before installing or updating them.
