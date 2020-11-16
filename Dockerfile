FROM python:3.8-slim

ARG GECKODRIVER_VERSION=v0.28.0
ARG FIREFOX_VERSION=82.0.3

WORKDIR /app

RUN apt-get update \
    && apt-get install -y wget bzip2 gconf-service libasound2 libatk1.0-0 libc6 \
	libcairo2 libcups2 libdbus-1-3 libexpat1 libfontconfig1 libgcc1 \
	libgconf-2-4 libgdk-pixbuf2.0-0 libglib2.0-0 libgtk-3-0 libnspr4 \
	libpango-1.0-0 libpangocairo-1.0-0 libstdc++6 libx11-6 libx11-xcb1 \
	libxcb1 libxcomposite1 libxcursor1 libxdamage1 libxext6 libxfixes3 \
	libxi6 libxrandr2 libxrender1 libxss1 libxtst6 ca-certificates \
	fonts-liberation libappindicator1 libnss3 lsb-release xdg-utils \
    && rm -rf /var/lib/apt/lists/*

RUN wget https://github.com/mozilla/geckodriver/releases/download/${GECKODRIVER_VERSION}/geckodriver-${GECKODRIVER_VERSION}-linux64.tar.gz \
    && tar -xvf geckodriver-${GECKODRIVER_VERSION}-linux64.tar.gz && mv geckodriver /usr/local/bin \
    && wget https://download-installer.cdn.mozilla.net/pub/firefox/releases/${FIREFOX_VERSION}/linux-x86_64/en-US/firefox-${FIREFOX_VERSION}.tar.bz2 \
    && tar -xvjf firefox-${FIREFOX_VERSION}.tar.bz2 && mv firefox /opt && ln -s /opt/firefox/firefox /usr/local/bin/firefox \
    && rm firefox* && rm geckodriver*

RUN pip install poetry

COPY poetry.lock /app
COPY pyproject.toml /app
RUN poetry install --no-dev

COPY examples /app
COPY seekout /app/seekout
COPY .env /app

CMD ["poetry", "run", "python", "RTX_30xx.py"]
