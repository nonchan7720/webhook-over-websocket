FROM golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d

RUN apt-get update \
  && apt-get install --no-install-recommends -y \
  make \
  sudo \
  vim \
  procps \
  curl \
  openssh-client \
  gettext \
  libexpat1-dev \
  libz-dev \
  libssl-dev \
  libcurl4-openssl-dev \
  lsb-release \
  locales \
  less \
  mandoc \
  && localedef -f UTF-8 -i ja_JP ja_JP.utf8 \
  && go install github.com/cweill/gotests/gotests@v1.9.0 \
  && go install github.com/fatih/gomodifytags@v1.17.0 \
  && go install github.com/josharian/impl@v1.2.0 \
  && go install github.com/haya14busa/goplay/cmd/goplay@v1.0.0 \
  && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 \
  && go install golang.org/x/tools/gopls@v0.20.0 \
  && go install github.com/go-delve/delve/cmd/dlv@v1.25.2

# cleanup
RUN rm -rf /root/.cache/* \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/*

ENV APP_DIR=/src

ARG GIT_VERSION=2.48.1
RUN cd /usr/local/src \
  && wget https://github.com/git/git/archive/refs/tags/v${GIT_VERSION}.tar.gz \
  && tar -xzf v${GIT_VERSION}.tar.gz \
  && cd git-${GIT_VERSION} \
  && make prefix=/usr/local all -j "$(nproc)" \
  && make prefix=/usr/local install

COPY --from=traefik:v3.7.19@sha256:a6c0194dda77bcb804824580f36fa7edc56e315b1acfe933366c8b52844a78a4 /usr/local/bin/traefik /usr/local/bin/traefik

USER $USER_NAME

ENV TZ=Asia/Tokyo

WORKDIR ${APP_DIR}
