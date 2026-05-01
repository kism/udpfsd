# Stage 1: Build libchdr from source
FROM debian:trixie AS libchdr-builder
ARG LIBCHDR_VERSION=0.3.0

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    cmake \
    unzip \
    && rm -rf /var/lib/apt/lists/*

ADD https://codeload.github.com/rtissera/libchdr/zip/refs/tags/v${LIBCHDR_VERSION} /tmp/libchdr.zip

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
FROM golang:1.26 AS go-builder

COPY --from=libchdr-builder /opt/libchdr /opt/libchdr

ARG VERSION=unknown
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/opt/libchdr/include"
ENV CGO_LDFLAGS="-L/opt/libchdr/lib"

WORKDIR /src
COPY . .

RUN go build \
    -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /udpfsd \
    ./cmd/udpfsd


# Stage 3: Download and extract Neutrino
FROM debian:trixie AS neutrino-downloader

RUN apt-get update && apt-get install -y --no-install-recommends \
    p7zip \
    && rm -rf /var/lib/apt/lists/*

# Later we can make this an argument, but right now we need this pre-release
ADD https://github.com/rickgaiser/neutrino/releases/download/latest/neutrino_v1.8.0-37-g3ea72bb.7z /tmp/neutrino.7z

WORKDIR /tmp/neutrino
RUN 7z x /tmp/neutrino.7z
RUN rm -rf /tmp/neutrino/udpfs_server

# Stage 4: Runtime image
FROM debian:trixie

COPY --from=libchdr-builder /opt/libchdr/lib /opt/libchdr/lib
COPY --from=go-builder /udpfsd /usr/local/bin/udpfsd
COPY --from=neutrino-downloader /tmp/neutrino /data/neutrino

RUN echo /opt/libchdr/lib > /etc/ld.so.conf.d/libchdr.conf && ldconfig

EXPOSE 62966/udp

ENTRYPOINT ["/usr/local/bin/udpfsd"]
