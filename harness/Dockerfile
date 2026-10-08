FROM python:3.12-slim
WORKDIR /app
COPY requirements.lock /app/requirements.lock
RUN pip install --no-cache-dir -r requirements.lock
COPY runtime.py /app/runtime.py
USER 65532:65532
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HARNESS_BIND=0.0.0.0 OPENAI_AGENTS_DISABLE_TRACING=1
EXPOSE 8811
CMD ["python", "runtime.py"]
