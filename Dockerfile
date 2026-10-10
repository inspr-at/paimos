# SPDX-License-Identifier: AGPL-3.0-only
# Compilation happens on the host in scripts/build-image-inputs.mjs.
# aeon-runtime is a digest-bound OCI named context, prepared separately using
# scripts/Dockerfile.runtime (chromium=152.0.7977.82-r0, tini=0.19.0-r3).
# No network access or compilation is needed to assemble this image.
FROM aeon-runtime
# No instruction reads VERSION: the version is linked into the binary on the
# host. It stays declared so smoke, proof, provenance export, push and the
# standalone build keep one identical build-arg set without an unconsumed arg.
ARG VERSION=dev
COPY dist/image-input/paimos /paimos
COPY NOTICE /usr/share/doc/aeon/NOTICE
# The runtime UID/GID is a contract with every host: writable file mounts
# must be owned by 65532 (the former distroless nonroot user). Never let it float.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/paimos", "serve"]
