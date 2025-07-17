# 🚨 Trivy Vulnerability Report (High/Critical)

| Target | Package | Severity | Title | CVE | Installed | Fixed |
|--------|---------|----------|-------|-----|-----------|-------|
| go.mod | github.com/docker/docker | CRITICAL | moby: Authz zero length regression | CVE-2024-41110 | v24.0.7+incompatible | 23.0.15, 26.1.5, 27.1.1, 25.0.6 |
| go.mod | golang.org/x/crypto | CRITICAL | golang.org/x/crypto/ssh: Misuse of ServerConfig.PublicKeyCallback may cause authorization bypass in golang.org/x/crypto | CVE-2024-45337 | v0.17.0 | 0.31.0 |
| go.mod | golang.org/x/crypto | HIGH | golang.org/x/crypto/ssh: Denial of Service in the Key Exchange of golang.org/x/crypto/ssh | CVE-2025-22869 | v0.17.0 | 0.35.0 |
