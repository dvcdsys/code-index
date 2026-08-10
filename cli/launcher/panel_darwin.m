// panel_darwin.m — the AppKit half of the menu bar panel.
//
// This file owns exactly three things: the NSStatusItem in the menu bar, a
// borderless NSPanel that drops from it, and the WKWebView inside that panel.
// Everything the panel SHOWS comes from Go as one JSON state object
// (panel_set_state); everything the user DOES goes back to Go as one JSON
// action (goPanelAction). No decisions are made here — this is a projector.
//
// Why a webview and not native views: the design (mac/design spec) is a dense,
// bordered, token-driven layout — square toggles, a segmented progress bar,
// full-bleed 1.5pt dividers — that AppKit controls cannot be styled into.
// Drawing it as custom NSViews means reimplementing layout, hover states and
// dark mode by hand in a language this project otherwise does not use; HTML
// does all of that natively, follows the system appearance for free
// (prefers-color-scheme), and keeps the entire look in one reviewable file.
// The webview loads a single embedded HTML string. No network access, no
// remote content, no JavaScript beyond our own.
//
// Memory: compiled without ARC (cgo passes CFLAGS to the generated C too, and
// -fobjc-arc does not belong there). Every object stored in a static below is
// created with alloc/init or explicitly retained, and lives for the process —
// this app has exactly one panel and never tears it down.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

extern void goPanelAction(const char *json);
extern void goPanelReady(void);
extern void goPanelExit(void);

// The panel width is fixed by the design; height follows the content, reported
// by JavaScript after every render.
static const CGFloat kPanelWidth = 392;
// Gap between the menu bar and the panel's top edge.
static const CGFloat kPanelGap = 6;

@interface CixPanel : NSPanel
@end

@implementation CixPanel
// A borderless window refuses key status by default, and without it there are
// no keyboard events — no Esc to close, no Tab through controls.
- (BOOL)canBecomeKeyWindow {
    return YES;
}
@end

@interface CixController
    : NSObject <NSApplicationDelegate, NSWindowDelegate,
                WKScriptMessageHandler, WKNavigationDelegate>
@end

static CixController *controller;
static NSStatusItem *statusItem;
static CixPanel *panel;
static WKWebView *webView;
static NSString *pendingHTML;
static NSString *pendingIconPath;
static NSString *pendingState; // last state JSON, replayed on load
static BOOL webLoaded = NO;
static id clickMonitor;

@implementation CixController

- (void)applicationDidFinishLaunching:(NSNotification *)note {
    // LSUIElement in Info.plist already makes this an accessory app; set it
    // explicitly too so `go run` outside a bundle behaves the same.
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

    statusItem = [[[NSStatusBar systemStatusBar]
        statusItemWithLength:NSVariableStatusItemLength] retain];
    // imageNamed finds cixTemplate.png + cixTemplate@2x.png in the bundle and
    // pairs them into one multi-representation image; the path is the fallback
    // for running outside a bundle during development.
    NSImage *icon = [[NSImage imageNamed:@"cixTemplate"] retain];
    if (icon == nil) {
        icon = [[NSImage alloc] initWithContentsOfFile:pendingIconPath];
    }
    if (icon != nil) {
        // Template: macOS recolours it for dark mode and the pressed state
        // from the alpha channel alone. Never tint it manually.
        [icon setTemplate:YES];
        [icon setSize:NSMakeSize(18, 18)];
        statusItem.button.image = icon;
        statusItem.button.imagePosition = NSImageLeft;
    } else {
        statusItem.button.title = @"cix";
    }
    statusItem.button.target = self;
    statusItem.button.action = @selector(togglePanel:);

    [self buildPanel];

    // Dismiss on any click outside the app. A global monitor never sees our
    // own events, so clicks inside the panel are unaffected.
    clickMonitor = [[NSEvent
        addGlobalMonitorForEventsMatchingMask:(NSEventMaskLeftMouseDown |
                                               NSEventMaskRightMouseDown)
                                      handler:^(NSEvent *e) {
                                        [self closePanel];
                                      }] retain];

    goPanelReady();
}

- (void)applicationWillTerminate:(NSNotification *)note {
    goPanelExit();
}

- (void)buildPanel {
    panel = [[CixPanel alloc]
        initWithContentRect:NSMakeRect(0, 0, kPanelWidth, 200)
                  styleMask:(NSWindowStyleMaskBorderless |
                             NSWindowStyleMaskNonactivatingPanel)
                    backing:NSBackingStoreBuffered
                      defer:NO];
    panel.opaque = NO;
    panel.backgroundColor = [NSColor clearColor];
    // The system shadow, not a CSS one: it wraps the webview's opaque rounded
    // rectangle exactly, and CSS shadows would need dead margins around the
    // window to bleed into.
    panel.hasShadow = YES;
    panel.level = NSPopUpMenuWindowLevel;
    panel.collectionBehavior = (NSWindowCollectionBehaviorCanJoinAllSpaces |
                                NSWindowCollectionBehaviorFullScreenAuxiliary);
    panel.hidesOnDeactivate = NO;
    panel.animationBehavior = NSWindowAnimationBehaviorNone;
    panel.delegate = self; // for windowDidResignKey below

    WKWebViewConfiguration *cfg =
        [[[WKWebViewConfiguration alloc] init] autorelease];
    [cfg.userContentController addScriptMessageHandler:self name:@"cix"];
    webView = [[WKWebView alloc] initWithFrame:panel.contentView.bounds
                                 configuration:cfg];
    // Transparent chrome: the HTML draws the panel's rounded border itself, so
    // the window must not paint white behind its corners. Private-ish but
    // stable KVC key, the standard way to do this from outside WebKit.
    [webView setValue:@NO forKey:@"drawsBackground"];
    webView.navigationDelegate = self;
    webView.autoresizingMask = (NSViewWidthSizable | NSViewHeightSizable);
    // No back/forward, no context menu content worth keeping — but the default
    // menu on a right-click exposes "Reload", which would blank the panel
    // until the next state push. Harmless, so not worth suppressing further.
    panel.contentView = webView;

    [webView loadHTMLString:pendingHTML baseURL:nil];
}

- (void)togglePanel:(id)sender {
    if (panel.visible) {
        [self closePanel];
    } else {
        [self openPanel];
    }
}

- (void)openPanel {
    NSWindow *bar = statusItem.button.window;
    if (bar == nil) {
        return;
    }
    NSRect anchor = bar.frame; // already in screen coordinates
    NSScreen *screen = bar.screen ?: [NSScreen mainScreen];

    CGFloat x = NSMidX(anchor) - kPanelWidth / 2;
    // Keep the panel on the screen it opened from, with the same 8pt breathing
    // room the system gives its own menus.
    CGFloat maxX = NSMaxX(screen.visibleFrame) - kPanelWidth - 8;
    if (x > maxX) {
        x = maxX;
    }
    if (x < NSMinX(screen.visibleFrame) + 8) {
        x = NSMinX(screen.visibleFrame) + 8;
    }
    [panel setFrameTopLeftPoint:NSMakePoint(x, NSMinY(anchor) - kPanelGap)];

    statusItem.button.highlighted = YES;
    [panel makeKeyAndOrderFront:nil];
    [panel invalidateShadow];

    // Tell Go the panel is being looked at: it answers with a fresh poll, so
    // the uptime and status shown are seconds old, not up to a tick old.
    goPanelAction("{\"action\":\"opened\"}");
}

- (void)closePanel {
    if (!panel.visible) {
        return;
    }
    [panel orderOut:nil];
    statusItem.button.highlighted = NO;
}

// windowDidResignKey — clicking anything that takes key status away (another
// app, a dialog this app opens) closes the panel, matching menu behaviour.
- (void)windowDidResignKey:(NSNotification *)note {
    if (note.object == panel) {
        [self closePanel];
    }
}

- (void)setStateJSON:(NSString *)json {
    [pendingState release];
    pendingState = [json retain];
    if (!webLoaded) {
        return; // replayed from didFinishNavigation
    }
    [self pushState];
}

- (void)pushState {
    if (pendingState == nil) {
        return;
    }
    NSString *js =
        [NSString stringWithFormat:@"window.cixRender && window.cixRender(%@)",
                                   pendingState];
    [webView evaluateJavaScript:js completionHandler:nil];
}

- (void)setTitle:(NSString *)title {
    statusItem.button.title = title;
}

- (void)webView:(WKWebView *)wv
    didFinishNavigation:(WKNavigation *)nav {
    webLoaded = YES;
    [self pushState];
}

- (void)userContentController:(WKUserContentController *)ucc
      didReceiveScriptMessage:(WKScriptMessage *)message {
    if (![message.body isKindOfClass:[NSDictionary class]]) {
        return;
    }
    NSDictionary *body = message.body;
    NSString *action = body[@"action"];

    // Layout messages are handled here — they are about this window, not about
    // the server, and Go has no business resizing NSWindows.
    if ([action isEqualToString:@"height"]) {
        CGFloat h = [body[@"value"] doubleValue];
        if (h < 40 || h > 1200) {
            return;
        }
        NSRect f = panel.frame;
        CGFloat top = NSMaxY(f);
        f.size.height = h;
        f.origin.y = top - h;
        // No animation: the height changes either on a state flip (where the
        // design calls for a crossfade, done in CSS) or before the panel is
        // even visible.
        [panel setFrame:f display:YES];
        [panel invalidateShadow];
        return;
    }
    if ([action isEqualToString:@"close"]) {
        [self closePanel];
        return;
    }

    NSData *data = [NSJSONSerialization dataWithJSONObject:body
                                                   options:0
                                                     error:nil];
    if (data == nil) {
        return;
    }
    NSString *json = [[[NSString alloc] initWithData:data
                                            encoding:NSUTF8StringEncoding]
        autorelease];

    // Button-shaped actions dismiss the panel like a menu item would; toggle
    // rows keep it open so the switch is seen doing its work. The distinction
    // lives in the HTML (dismiss:true), not in a hardcoded list here.
    if ([body[@"dismiss"] boolValue]) {
        [self closePanel];
    }
    goPanelAction(json.UTF8String);
}

@end

// --- C API, called from Go. Every entry point hops to the main thread: AppKit
// is main-thread-only and Go calls these from arbitrary goroutines.

void panel_run(const char *iconPath, const char *html) {
    @autoreleasepool {
        pendingIconPath = [[NSString stringWithUTF8String:iconPath] retain];
        pendingHTML = [[NSString stringWithUTF8String:html] retain];
        NSApplication *app = [NSApplication sharedApplication];
        controller = [[CixController alloc] init];
        app.delegate = controller;
        [app run];
    }
}

void panel_set_state(const char *json) {
    NSString *s = [NSString stringWithUTF8String:json];
    dispatch_async(dispatch_get_main_queue(), ^{
      [controller setStateJSON:s];
    });
}

void panel_set_title(const char *title) {
    NSString *s = [NSString stringWithUTF8String:title];
    dispatch_async(dispatch_get_main_queue(), ^{
      [controller setTitle:s];
    });
}

void panel_quit(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
      [NSApp terminate:nil];
    });
}
