# Security

Please report vulnerabilities privately through GitHub's
[security advisories](https://github.com/AndreMuraro/recon/security/advisories/new),
not in public issues.

recon connects to warehouses with credentials you provide and reads the tables you name.
It never writes to them.

Releases are built in GitHub Actions and carry build provenance. To verify a download:

```console
gh attestation verify recon_<version>_<os>_<arch>.tar.gz --repo AndreMuraro/recon
```
