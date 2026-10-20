# Supply a reviewed immutable python image digest at build time.
ARG PYTHON_IMAGE
FROM ${PYTHON_IMAGE}
ARG PYTHON_IMAGE
RUN case "$PYTHON_IMAGE" in *@sha256:*) ;; *) exit 1 ;; esac
WORKDIR /worker
COPY requirements.txt .
RUN python -m pip install --no-cache-dir --require-hashes -r requirements.txt
COPY reader.py .
USER 10001:10001
ENTRYPOINT ["python", "-I", "-B", "/worker/reader.py"]
