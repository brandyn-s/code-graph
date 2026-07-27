from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from decimal import Decimal

from matched_depth_budget import ReservationLedger, allocate_decimal_budget


def test_decimal_shard_allocation_is_exact_and_deterministic() -> None:
    weights = {f"repo-{index:03d}": (index % 11) + 1 for index in range(99)}
    first = allocate_decimal_budget(Decimal("17.123456"), weights)
    second = allocate_decimal_budget(Decimal("17.123456"), weights)

    assert first == second
    assert sum(first.values(), start=Decimal("0")) == Decimal("17.123456")
    assert all(amount >= Decimal("0.000001") for amount in first.values())
    assert all(amount.as_tuple().exponent == -6 for amount in first.values())


def test_reservations_cannot_overshoot_under_concurrency() -> None:
    ledger = ReservationLedger(Decimal("1.000000"))

    def reserve_one(index: int):
        return ledger.reserve(f"operation-{index}", Decimal("0.020000"))

    with ThreadPoolExecutor(max_workers=16) as executor:
        reservations = list(executor.map(reserve_one, range(100)))

    accepted = [reservation for reservation in reservations if reservation is not None]
    assert len(accepted) == 50
    assert ledger.reserved_total == Decimal("1.000000")
    assert ledger.remaining == Decimal("0.000000")


def test_settlement_rejects_actual_cost_above_reserved_bound() -> None:
    ledger = ReservationLedger(Decimal("1.000000"))
    reservation = ledger.reserve("bounded-call", Decimal("0.200000"))
    assert reservation is not None

    try:
        ledger.settle(reservation, Decimal("0.200001"))
    except ValueError as exc:
        assert "exceeds reserved upper bound" in str(exc)
    else:
        raise AssertionError("settlement accepted cost above its reserved bound")

    assert ledger.reserved_total == Decimal("0.200000")
    assert ledger.spent_total == Decimal("0.000000")
