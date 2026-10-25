FROM python:3.12-slim
WORKDIR /app
COPY ai-agent/requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt && useradd --uid 10001 --create-home assistant
COPY ai-agent/planner.py ai-agent/server.py ./
COPY ai-agent/generated ./generated
USER assistant
CMD ["python", "server.py"]
