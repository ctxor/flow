from pathlib import Path
import secrets

root = Path(__file__).resolve().parent.parent
target = root / ".env"
if not target.exists():
    target.write_text((root / ".env.example").read_text().replace("replace-with-a-long-random-secret", secrets.token_hex(32)))
    target.chmod(0o600)
    print("Created .env with a random internal service credential.")
else:
    print("Existing .env preserved.")
