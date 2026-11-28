"""속성 기반 테스트."""
from decimal import Decimal

from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

from kqh.domain import Side
from kqh.state.tick_size import is_valid_tick, round_to_tick


@settings(max_examples=500, deadline=None, suppress_health_check=[HealthCheck.function_scoped_fixture])
@given(st.decimals(min_value=Decimal("1"), max_value=Decimal("3000000"), places=2))
def test_round_to_tick_valid_and_conservative(tick, price):
    """목적: 호가 보정 결과는 항상 유효 호가이며 매수는 ≤ 원가, 매도는 ≥ 원가
    입력: 1~3,000,000원 임의 가격
    기대: 불변조건 유지"""
    b = round_to_tick(price, tick, Side.BUY)
    s = round_to_tick(price, tick, Side.SELL)
    if b > 0:
        assert is_valid_tick(b, tick) and b <= price
    assert is_valid_tick(s, tick) and s >= price


@settings(max_examples=300, deadline=None)
@given(st.lists(st.integers(0, 10**7), min_size=10, max_size=10),
       st.lists(st.integers(0, 10**7), min_size=10, max_size=10))
def test_obi_bounded_or_none(bq, aq):
    """목적: OBI ∈ [-1,1] 또는 None(분모 0)
    입력: 임의 잔량
    기대: 범위 유지"""
    from conftest import NOW
    from kqh.domain import Venue
    from kqh.state.orderbook import BookUpdate, Level, OrderBook
    bids = tuple(Level(Decimal(70000 - 100 * i), q) for i, q in enumerate(bq))
    asks = tuple(Level(Decimal(70100 + 100 * i), q) for i, q in enumerate(aq))
    ob = OrderBook("005930", Venue.KRX)
    ob.apply(BookUpdate("005930", Venue.KRX, 1, NOW, NOW, bids, asks))
    v = ob.obi()
    assert (v is None) == (sum(bq) + sum(aq) == 0)
    assert v is None or -1 <= v <= 1
