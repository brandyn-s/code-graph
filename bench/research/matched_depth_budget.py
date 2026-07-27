"""Exact budget allocation and pre-call reservation primitives.

These helpers do not turn provider estimates into hard bounds. A caller may
reserve only a provider-enforced upper bound; absent such a bound, the paid
operation must not be dispatched.
"""
from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal, ROUND_FLOOR
from threading import Lock
from typing import Mapping


USD_QUANTUM = Decimal("0.000001")


def _usd(value: Decimal | str) -> Decimal:
    amount = value if isinstance(value, Decimal) else Decimal(value)
    if not amount.is_finite() or amount < 0:
        raise ValueError("USD amount must be finite and non-negative")
    if amount != amount.quantize(USD_QUANTUM):
        raise ValueError("USD amount must have at most six decimal places")
    return amount


def allocate_decimal_budget(
    total: Decimal | str,
    weights: Mapping[str, int],
) -> dict[str, Decimal]:
    """Allocate every micro-dollar exactly with deterministic largest remainder."""
    cap = _usd(total)
    if not weights:
        raise ValueError("at least one shard weight is required")
    if any(
        isinstance(weight, bool) or not isinstance(weight, int) or weight <= 0
        for weight in weights.values()
    ):
        raise ValueError("shard weights must be positive integers")

    cap_units = int(cap / USD_QUANTUM)
    weight_total = sum(weights.values())
    floor_units: dict[str, int] = {}
    remainders: list[tuple[Decimal, int, str]] = []
    for position, (name, weight) in enumerate(weights.items()):
        exact_units = Decimal(cap_units) * Decimal(weight) / Decimal(weight_total)
        units = int(exact_units.to_integral_value(rounding=ROUND_FLOOR))
        floor_units[name] = units
        remainders.append((exact_units - Decimal(units), position, name))

    unallocated = cap_units - sum(floor_units.values())
    remainders.sort(key=lambda item: (-item[0], item[1]))
    for _remainder, _position, name in remainders[:unallocated]:
        floor_units[name] += 1

    return {
        name: (Decimal(floor_units[name]) * USD_QUANTUM).quantize(USD_QUANTUM)
        for name in weights
    }


@dataclass(frozen=True)
class BudgetReservation:
    reservation_id: int
    label: str
    upper_bound: Decimal


class ReservationLedger:
    """Thread-safe pre-call reservations against proven operation bounds."""

    def __init__(self, cap: Decimal | str):
        self._cap = _usd(cap)
        self._lock = Lock()
        self._next_id = 1
        self._active: dict[int, BudgetReservation] = {}
        self._reserved_total = Decimal("0.000000")
        self._spent_total = Decimal("0.000000")

    @property
    def reserved_total(self) -> Decimal:
        with self._lock:
            return self._reserved_total

    @property
    def spent_total(self) -> Decimal:
        with self._lock:
            return self._spent_total

    @property
    def remaining(self) -> Decimal:
        with self._lock:
            return self._cap - self._reserved_total - self._spent_total

    def reserve(
        self,
        label: str,
        upper_bound: Decimal | str,
    ) -> BudgetReservation | None:
        amount = _usd(upper_bound)
        if amount <= 0:
            raise ValueError("reservation upper bound must be positive")
        if not label:
            raise ValueError("reservation label is required")
        with self._lock:
            remaining = self._cap - self._reserved_total - self._spent_total
            if amount > remaining:
                return None
            reservation = BudgetReservation(
                reservation_id=self._next_id,
                label=label,
                upper_bound=amount,
            )
            self._next_id += 1
            self._active[reservation.reservation_id] = reservation
            self._reserved_total += amount
            return reservation

    def settle(
        self,
        reservation: BudgetReservation,
        actual_cost: Decimal | str,
    ) -> None:
        actual = _usd(actual_cost)
        with self._lock:
            active = self._active.get(reservation.reservation_id)
            if active != reservation:
                raise ValueError("reservation is not active")
            if actual > reservation.upper_bound:
                raise ValueError(
                    "actual cost exceeds reserved upper bound; provider bound "
                    "or metering evidence is invalid"
                )
            del self._active[reservation.reservation_id]
            self._reserved_total -= reservation.upper_bound
            self._spent_total += actual
