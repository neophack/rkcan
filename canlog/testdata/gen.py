"""Generate BLF/ASC fixtures with python-can (independent reference
implementation) plus JSON describing the expected frames.

    pip install python-can && python3 gen.py
"""
import json, random
import can

random.seed(42)
msgs = []
t = 1000.0
for i in range(6000):
    t += random.choice([0.0001, 0.001, 0.0123])
    kind = i % 6
    fd = kind in (3, 4, 5)
    ext = i % 7 == 0
    remote = (kind == 2 and i % 4 == 0)
    if fd:
        n = random.choice([0, 1, 8, 12, 16, 20, 24, 32, 48, 64])
    else:
        n = 0 if remote else random.randint(0, 8)
    data = bytes(random.randrange(256) for _ in range(n))
    msgs.append(can.Message(
        timestamp=t,
        arbitration_id=random.randrange(1 << 29) if ext else random.randrange(1 << 11),
        is_extended_id=ext,
        is_remote_frame=remote,
        is_fd=fd,
        bitrate_switch=fd and kind != 5,
        error_state_indicator=fd and kind == 4,
        is_rx=i % 5 != 0,
        dlc=n if not fd else len(data),
        data=data,
        channel=(i % 2) + 1,
    ))

def expected(ms):
    t0 = ms[0].timestamp
    return [dict(
        t=round(m.timestamp - t0, 6), ch=m.channel, id=m.arbitration_id, ext=m.is_extended_id,
        rtr=m.is_remote_frame, fd=m.is_fd, brs=m.bitrate_switch, esi=m.error_state_indicator,
        tx=not m.is_rx, data=m.data.hex() if not m.is_remote_frame else "") for m in ms]

with can.BLFWriter("sample.blf") as w:
    for m in msgs:
        w.on_message_received(m)
with can.ASCWriter("sample.asc") as w:
    for m in msgs[:800]:
        w.on_message_received(m)

json.dump(expected(msgs), open("sample.blf.json", "w"))
json.dump(expected(msgs[:800]), open("sample.asc.json", "w"))

# Re-read with python-can to confirm the fixtures round-trip there too
assert len(list(can.BLFReader("sample.blf"))) == len(msgs)
assert len(list(can.ASCReader("sample.asc"))) == 800
