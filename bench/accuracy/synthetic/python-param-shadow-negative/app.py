"""Negative fixture: a bare call to a parameter must not bind to the
module-level function of the same name (upstream 95689b5c)."""


def cb():
    return 1


def helper():
    return 2


def run_with(cb):
    # `cb` here is the parameter; resolving this to module-level `cb` is a
    # phantom edge by construction.
    return cb()


def outer(run):
    def inner():
        return run()
    return inner()


def main():
    return run_with(helper) + outer(helper) + helper()
