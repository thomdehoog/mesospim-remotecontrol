from PIL import Image, ImageDraw, ImageFont

W, H = 940, 640
img = Image.new("RGB", (W, H), "#ECECEC")
d = ImageDraw.Draw(img)


def font(names, size):
    for n in names:
        try:
            return ImageFont.truetype("C:/Windows/Fonts/" + n, size)
        except Exception:
            pass
    return ImageFont.load_default()


REG = ["segoeui.ttf", "arial.ttf"]
BOLD = ["segoeuib.ttf", "arialbd.ttf"]

f_title = font(BOLD, 14)
f_tab = font(REG, 12)
f_tab_b = font(BOLD, 12)
f_msg = font(REG, 14)
f_role = font(BOLD, 13)
f_conf = font(REG, 12)
f_conf_b = font(BOLD, 12)
f_ph = font(REG, 14)
f_hint = font(REG, 11)


def rrect(box, r, fill=None, outline=None, width=1):
    try:
        d.rounded_rectangle(box, radius=r, fill=fill, outline=outline, width=width)
    except Exception:
        d.rectangle(box, fill=fill, outline=outline, width=width)


def tw(s, f):
    return d.textlength(s, font=f)


# --- title bar ---
d.rectangle([0, 0, W, 34], fill="#2B2B2B")
d.text((14, 9), "mesoSPIM Control", font=f_title, fill="#FFFFFF")
for i, c in enumerate(["#27C93F", "#FFBD2E", "#FF5F56"]):
    cx = W - 22 - i * 22
    d.ellipse([cx - 6, 11, cx + 6, 23], fill=c)

# --- tab bar ---
tab_top, tab_bot = 34, 70
d.rectangle([0, tab_top, W, tab_bot], fill="#DDDDDD")
tabs = ["Acquisition", "Timelapse", "Remote Control", "AI Assistant"]
active = "AI Assistant"
x = 8
for t in tabs:
    w = tw(t, f_tab) + 28
    box = [x, tab_top + 4, x + w, tab_bot]
    if t == active:
        d.rectangle(box, fill="#F4F4F4")
        d.rectangle([x, tab_top + 4, x + w, tab_top + 7], fill="#1A73E8")
        d.text((x + 14, tab_top + 11), t, font=f_tab_b, fill="#1a1a1a")
    else:
        d.rectangle(box, fill="#CFCFCF")
        d.text((x + 14, tab_top + 11), t, font=f_tab, fill="#5f5f5f")
    x += w + 3

# --- content ---
d.rectangle([0, tab_bot, W, H], fill="#F4F4F4")
pad = 16
left, right = pad, W - pad
in_h = 42
in_bot = H - pad
in_top = in_bot - in_h
out_top = tab_bot + pad
out_bot = in_top - 12

rrect([left, out_top, right, out_bot], 6, fill="#FFFFFF", outline="#C4C4C4", width=1)

tx = left + 16
ty = out_top + 14
inner_r = right - 16
lh = 30

lines = [
    ("user", "Move the stage to x = 12000, y = 4000 µm"),
    ("ai", "Moving the stage there…"),
    ("ai", "Done — stage now at X 12000 µm, Y 4000 µm."),
    ("user", "Now take a z-stack from 0 to 100 µm in 5 µm steps"),
    ("ai", "Setting z-start 0, z-end 100, step 5 µm  (21 planes)…"),
    ("ai", "Acquisition complete — 21 planes saved."),
    ("user", "What's the current objective and zoom?"),
    ("ai", "Objective 4× · Zoom 1.0× · Filter GFP."),
]

for kind, text in lines:
    if kind == "user":
        d.text((tx, ty), "You", font=f_role, fill="#1A73E8")
        d.text((tx + 44, ty), text, font=f_msg, fill="#202020")
        ty += lh
    elif kind == "ai":
        d.text((tx, ty), "AI", font=f_role, fill="#188038")
        d.text((tx + 44, ty), text, font=f_msg, fill="#303030")
        ty += lh
    else:  # confirm
        top = ty - 2
        rrect([tx - 4, top, inner_r, top + 30], 6, fill="#FFF4D6", outline="#E0A800", width=1)
        d.ellipse([tx + 6, top + 8, tx + 22, top + 24], fill="#E0A800")
        d.text((tx + 11, top + 7), "!", font=f_conf_b, fill="#FFFFFF")
        d.text((tx + 30, top + 8), "Confirm:", font=f_conf_b, fill="#7a5c00")
        w = tw("Confirm:", f_conf_b)
        d.text((tx + 30 + w + 8, top + 8), text, font=f_conf, fill="#5c4600")
        bw, bh = 62, 20
        by = top + 5
        bx2 = inner_r - 10 - bw
        bx1 = bx2 - 8 - bw
        rrect([bx1, by, bx1 + bw, by + bh], 4, fill="#188038")
        d.text((bx1 + (bw - tw("Allow", f_conf)) / 2, by + 3), "Allow", font=f_conf, fill="#FFFFFF")
        rrect([bx2, by, bx2 + bw, by + bh], 4, fill="#FFFFFF", outline="#c0392b", width=1)
        d.text((bx2 + (bw - tw("Deny", f_conf)) / 2, by + 3), "Deny", font=f_conf, fill="#c0392b")
        ty += 40

# --- input box ---
rrect([left, in_top, right, in_bot], 6, fill="#FFFFFF", outline="#1A73E8", width=2)
d.line([left + 14, in_top + 12, left + 14, in_bot - 12], fill="#1A73E8", width=1)  # caret
d.text((left + 22, in_top + 12), "Ask the microscope…", font=f_ph, fill="#9AA0A6")
hint = "Enter to send"
d.text((right - 14 - tw(hint, f_hint), in_top + 14), hint, font=f_hint, fill="#B0B0B0")

out = r"C:\Users\local_t.de4\Temp\claude\C--Users-t-de\9199edbd-1f89-41e9-ac98-b5081bb333ac\scratchpad\ai_assistant_mockup.png"
img.save(out)
print("saved", out)
