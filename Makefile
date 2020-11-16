IMAGE_NAME=seleniumpy

build:
	docker build -t $(IMAGE_NAME) .

dev:
	docker run -it $(IMAGE_NAME) sh

run:
	docker run -it $(IMAGE_NAME)
