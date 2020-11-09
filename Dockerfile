FROM python:3.8-slim

ARG GECKODRIVER_VERSION=v0.28.0
ARG FIREFOX_VERSION=82.0.3

RUN apt-get update \
    && apt-get install -y wget bzip2 \
    && rm -rf /var/lib/apt/lists/*

RUN wget https://github.com/mozilla/geckodriver/releases/download/${GECKODRIVER_VERSION}/geckodriver-${GECKODRIVER_VERSION}-linux64.tar.gz \
    && tar -xvf geckodriver-${GECKODRIVER_VERSION}-linux64.tar.gz && mv geckodriver /usr/local/bin \
    && wget https://download-installer.cdn.mozilla.net/pub/firefox/releases/${FIREFOX_VERSION}/linux-x86_64/en-US/firefox-${FIREFOX_VERSION}.tar.bz2 \
    && tar -xvjf firefox-${FIREFOX_VERSION}.tar.bz2 && mv firefox /opt && ln -s /opt/firefox/firefox /usr/local/bin/firefox \
    && rm firefox* && rm geckodriver*
