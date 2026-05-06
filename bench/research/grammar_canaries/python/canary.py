"""Canary fixture for the python tree-sitter grammar.

Exercises features the code-graph extractor depends on:
  - module-level + class-method functions
  - decorator
  - type annotations
  - default args
  - getattr-with-string-literal (INDIRECT_CALLS v0.2)
  - executor.submit (INDIRECT_CALLS v0.1)

If the AST shape for these constructs changes after a grammar update,
extraction quality changes — drift_check fires.
"""
from concurrent.futures import ThreadPoolExecutor


def module_level_function(arg: int = 0) -> str:
    return str(arg)


class Plugin:
    def handler_a(self) -> int:
        return 1

    def handler_b(self) -> int:
        return 2


def dispatcher(plugin: Plugin) -> int:
    return getattr(plugin, "handler_a")()


def submit_pattern(work: list[int]) -> None:
    with ThreadPoolExecutor() as ex:
        for w in work:
            ex.submit(module_level_function, w)


@staticmethod
def decorated_function() -> None:
    pass
