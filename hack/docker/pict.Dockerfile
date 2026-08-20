# Copyright 2026 Platform9, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
#
# microsoft/pict (https://github.com/microsoft/pict) publishes no Linux
# binary or container image of its own -- only source and a Windows .exe.
# homebrew-core ships a prebuilt Linux bottle for it, so this installs that
# instead of compiling PICT's C++ sources ourselves.
FROM homebrew/brew:4.6.20
ENV HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 HOMEBREW_NO_ENV_HINTS=1
RUN brew install pict
WORKDIR /var/pict
ENTRYPOINT ["pict"]
