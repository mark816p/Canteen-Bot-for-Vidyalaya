from pathlib import Path
import runpy


runpy.run_path(str(Path(__file__).resolve().parent / "nss" / "main.py"), run_name="__main__")
