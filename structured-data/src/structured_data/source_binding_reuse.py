"""Bounded in-process reuse of successful, exact source-binding reviews.

No data rows, failures or business decisions are inferred by this cache. Callers
must still validate every returned review against the live authorized catalogue.
"""
from collections import OrderedDict
from concurrent.futures import Future, TimeoutError
from copy import deepcopy
import json
from threading import Lock
import time


class CallableIdentity:
    """Retain the callable so a later object's reused id cannot hit its cache."""
    def __init__(self, value):
        self.value = value

    def __hash__(self):
        return id(self.value)

    def __eq__(self, other):
        return isinstance(other, CallableIdentity) and self.value is other.value


class SourceBindingReuse:
    def __init__(self, capacity=128, ttl=300, clock=time.monotonic):
        self.capacity, self.ttl, self.clock = capacity, ttl, clock
        self._entries = OrderedDict()
        self._pending = {}
        self._lock = Lock()

    def run(self, key, compute, reusable, deadline):
        with self._lock:
            now = self.clock()
            for expired in [k for k, (until, _) in self._entries.items() if until <= now]:
                del self._entries[expired]
            if key in self._entries:
                self._entries.move_to_end(key)
                return deepcopy(self._entries[key][1])
            pending = self._pending.get(key)
            owner = pending is None
            if owner and len(self._pending) < self.capacity:
                pending = self._pending[key] = Future()
        # Under saturation, execute normally rather than retaining unbounded
        # in-flight state or extending a request's existing time budget.
        if pending is None:
            return compute()
        if not owner:
            try:
                return deepcopy(pending.result(timeout=max(0, deadline - self.clock())))
            except TimeoutError:
                raise ValueError('来源模型核验超出本次时间预算；未执行取数') from None
        try:
            value = compute()
            # Keep memory bounded even for an unusually large valid receipt.
            cacheable = reusable(value) and len(json.dumps(value, ensure_ascii=False).encode()) <= 32768
            with self._lock:
                if cacheable:
                    self._entries[key] = (self.clock() + self.ttl, deepcopy(value))
                    self._entries.move_to_end(key)
                    while len(self._entries) > self.capacity:
                        self._entries.popitem(last=False)
                self._pending.pop(key, None)
            pending.set_result(deepcopy(value))
            return value
        except BaseException as error:
            with self._lock:
                self._pending.pop(key, None)
            pending.set_exception(error)
            raise
