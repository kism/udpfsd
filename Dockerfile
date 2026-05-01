# Stage 1: Build libchdr from source
FROM debian:trixie AS libchdr-builder
ARG LIBCHDR_VERSION=0.3.0

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    cmake \
    unzip \
    wget \
    && rm -rf /var/lib/apt/lists/*

RUN --mount=type=cache,target=/var/cache/downloads \
    wget -q -nc -P /var/cache/downloads \
        https://codeload.github.com/rtissera/libchdr/zip/refs/tags/v${LIBCHDR_VERSION} \
    && cp /var/cache/downloads/v${LIBCHDR_VERSION} /tmp/libchdr.zip

RUN cd /tmp \
    && unzip libchdr.zip \
    && cmake -S libchdr-${LIBCHDR_VERSION} -B libchdr-build \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX=/opt/libchdr \
        -DBUILD_SHARED_LIBS=ON \
        -DBUILD_TESTING=OFF \
    && cmake --build libchdr-build -j"$(nproc)" \
    && cmake --install libchdr-build

# Stage 2: Build the Go application
FROM golang:1.25 AS go-builder

COPY --from=libchdr-builder /opt/libchdr /opt/libchdr

ARG VERSION=unknown
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/opt/libchdr/include"
ENV CGO_LDFLAGS="-L/opt/libchdr/lib"

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/root/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build \
        -ldflags="-s -w -X main.Version=${VERSION}" \
        -o /udpfsd \
        ./cmd/udpfsd


# Stage 3: Download and extract Neutrino
FROM debian:trixie AS neutrino-downloader

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    p7zip \
    wget \
    && rm -rf /var/lib/apt/lists/*

# Later we can make this an argument, but right now we need this pre-release
RUN --mount=type=cache,target=/var/cache/downloads \
    wget -q -nc -P /var/cache/downloads \
        https://github.com/rickgaiser/neutrino/releases/download/latest/neutrino_v1.8.0-37-g3ea72bb.7z \
    && cp /var/cache/downloads/neutrino_v1.8.0-37-g3ea72bb.7z /tmp/neutrino.7z

WORKDIR /tmp/neutrino
RUN 7z x /tmp/neutrino.7z
RUN rm -rf /tmp/neutrino/udpfs_server

# Stage 4: Runtime image
FROM debian:trixie

COPY --from=libchdr-builder /opt/libchdr/lib /opt/libchdr/lib
COPY --from=go-builder /udpfsd /usr/local/bin/udpfsd
COPY --from=neutrino-downloader /tmp/neutrino /data/neutrino

RUN echo /opt/libchdr/lib > /etc/ld.so.conf.d/libchdr.conf && ldconfig

COPY <<'EOF' /entrypoint.sh
#!/bin/sh
set -e
if [ -n "$PS2_IP" ]; then
    echo "Updating Neutrino config files with PS2 IP: $PS2_IP"
    grep -rl "ip=[0-9.]" /data/neutrino/config | while read -r f; do
        sed -i "s/ip=[0-9.]\+/ip=${PS2_IP}/g" "$f"
        echo "  Updated: $f"
    done
fi
exec /usr/local/bin/udpfsd "$@"
EOF
RUN chmod +x /entrypoint.sh

# EXPOSE 62966/udp
# EXPOSE 62967/udp

ENTRYPOINT ["/entrypoint.sh"]
