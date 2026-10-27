FROM golang:1.27.3-trixie@sha256:fbb7ae40b62ff8afe31662df570d39b4f5052ac32b90dae8e60d7657a20a74f4

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

COPY --from=traefik:v3.7.15@sha256:0f6150202bc1fed72a7bd2107460eaa510e5b8a905f3fdb5b84f2c8619c33368 /usr/local/bin/traefik /usr/local/bin/traefik

USER $USER_NAME

ENV TZ=Asia/Tokyo

WORKDIR ${APP_DIR}
