// The type specimen (cmd/image typespec.go puts it in the guest and runs it with gjs -m): the same
// text every run, drawn by GTK 4 at the UI's sizes, regular and bold, in Adwaita Sans and Adwaita
// Mono, light on dark and dark on light, fullscreen so it is where it was at every scale. It
// writes where each line landed, and the settings GTK resolved, to the file named by its first
// argument; with "on" as its second it turns GtkSettings' gtk-hint-font-metrics on, which no
// GSettings key reaches.
//
// Plain GTK, no libadwaita: the text is drawn as every GTK 4 app draws it, with no widget's style
// between the label and the screen. Its CSS outranks the user's gtk.css (the theme), which would
// otherwise colour the labels.
import Gtk from 'gi://Gtk?version=4.0';
import Gdk from 'gi://Gdk?version=4.0';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Pango from 'gi://Pango';
import System from 'system';

const [outPath, metrics] = System.programArgs;

const SIZES = [9, 10, 11, 13, 15, 20];
const WEIGHTS = [400, 700];
const FAMILIES = [['sans', 'Adwaita Sans'], ['mono', 'Adwaita Mono']];
const THEMES = [['dark', '#E6EDF2', '#172028'], ['light', '#11181E', '#F4F6F8']];
// What is measured: stems ('l' and 'I' one each, 'H' two), flat tops and bottoms at the x-height
// and the baseline ('x', 'z'), and a word to look at. Spaced, so every glyph is its own ink.
const LINES = [['stems', 'l l I I H H'], ['align', 'x z x z x z'], ['word', 'Hamburgefonstiv']];
const PANGRAM = 'Sphinx of black quartz, judge my vow. 0123456789 Il1 O0';

const css = new Gtk.CssProvider();
css.load_from_string(
    THEMES.map(([t, fg, bg]) => `.${t} { background-color: ${bg}; } .${t} label { color: ${fg}; }`).join('\n') +
    '\nlabel { padding: 0; margin: 0; min-height: 0; text-shadow: none; }');
// Above GTK_STYLE_PROVIDER_PRIORITY_USER (800), where ~/.config/gtk-4.0/gtk.css is.
const PRIORITY = 900;

const esc = s => GLib.markup_escape_text(s, -1);
const markup = (family, weight, size, text) =>
    `<span font_family="${family}" font_weight="${weight}" size="${size}pt">${esc(text)}</span>`;

function quadrant(theme, fam, items) {
    const [name, family] = fam;
    const grid = new Gtk.Grid({column_spacing: 24, row_spacing: 4, margin_top: 10, margin_bottom: 10, margin_start: 16, margin_end: 16});
    let row = 0;
    for (const weight of WEIGHTS) {
        for (const size of SIZES) {
            LINES.forEach(([kind, text], col) => {
                const l = new Gtk.Label({use_markup: true, label: markup(family, weight, size, text), xalign: 0, halign: Gtk.Align.START});
                grid.attach(l, col, row, 1, 1);
                items.push({label: l, theme: theme[0], fg: theme[1], bg: theme[2], family: name, weight, size, kind});
            });
            row++;
        }
    }
    const p = new Gtk.Label({use_markup: true, label: markup(family, 400, 11, PANGRAM), xalign: 0, halign: Gtk.Align.START});
    grid.attach(p, 0, row, LINES.length, 1);
    items.push({label: p, theme: theme[0], fg: theme[1], bg: theme[2], family: name, weight: 400, size: 11, kind: 'pangram'});
    return grid;
}

const app = new Gtk.Application({application_id: 'io.github.construct_linux.TypeSpecimen', flags: Gio.ApplicationFlags.NON_UNIQUE});
app.connect('activate', () => {
    const settings = Gtk.Settings.get_default();
    if (metrics === 'on')
        settings.gtk_hint_font_metrics = true;
    Gtk.StyleContext.add_provider_for_display(Gdk.Display.get_default(), css, PRIORITY);
    const win = new Gtk.Window({application: app, title: 'type-specimen'});
    const items = [];
    const root = new Gtk.Box({orientation: Gtk.Orientation.VERTICAL, homogeneous: true});
    for (const theme of THEMES) {
        const half = new Gtk.Box({orientation: Gtk.Orientation.HORIZONTAL, homogeneous: true});
        half.add_css_class(theme[0]);
        for (const fam of FAMILIES)
            half.append(quadrant(theme, fam, items));
        root.append(half);
    }
    win.set_child(root);
    win.connect('map', () => {
        // Drawn, and fullscreen, well before the harness looks.
        GLib.timeout_add(GLib.PRIORITY_DEFAULT, 1500, () => {
            const [sx, sy] = win.get_surface_transform();
            const fonts = {};
            for (const [name, family] of FAMILIES) {
                const ctx = items[0].label.get_pango_context();
                const f = ctx.load_font(Pango.FontDescription.from_string(`${family} 11`));
                fonts[name] = f ? f.describe().to_string() : '';
            }
            const out = {
                fullscreen: win.is_fullscreen(),
                surface: [sx, sy],
                env: GLib.getenv('FREETYPE_PROPERTIES') ?? '',
                settings: {
                    font_rendering: settings.gtk_font_rendering === Gtk.FontRendering.MANUAL ? 'manual' : 'automatic',
                    xft_hinting: settings.gtk_xft_hinting,
                    xft_hintstyle: settings.gtk_xft_hintstyle ?? '',
                    xft_antialias: settings.gtk_xft_antialias,
                    xft_rgba: settings.gtk_xft_rgba ?? '',
                    hint_font_metrics: settings.gtk_hint_font_metrics,
                    xft_dpi: settings.gtk_xft_dpi,
                },
                fonts,
                items: items.map(i => {
                    const [ok, b] = i.label.compute_bounds(win);
                    return {theme: i.theme, fg: i.fg, bg: i.bg, family: i.family, weight: i.weight, size: i.size, kind: i.kind,
                        x: ok ? b.get_x() : 0, y: ok ? b.get_y() : 0, w: ok ? b.get_width() : 0, h: ok ? b.get_height() : 0};
                }),
            };
            GLib.file_set_contents(outPath, JSON.stringify(out));
            return GLib.SOURCE_REMOVE;
        });
    });
    win.fullscreen();
    win.present();
});
app.run([System.programInvocationName]);
