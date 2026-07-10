const fs = require('fs');
const path = require('path');
const qrcode = require('qrcode-terminal');
const pino = require('pino');

const logger = pino({ level: 'silent' });
const mode = process.argv[2]; // 'link', 'test', or 'send'
const AUTH_DIR = path.join(__dirname, '.baileys_auth');

console.log(`[Sender] Starting in '${mode}' mode...`);

async function main() {
    let baileys;
    try {
        baileys = await import('@whiskeysockets/baileys');
        console.log('[Sender] Baileys imported successfully.');
    } catch (err) {
        console.error('[Sender] Failed to import Baileys:', err);
        process.exit(1);
    }

    const {
        default: makeWASocket,
        useMultiFileAuthState,
        DisconnectReason,
        fetchLatestBaileysVersion,
        makeCacheableSignalKeyStore
    } = baileys;

    const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);
    console.log('[Sender] Auth state loaded.');

    let version;
    try {
        const result = await fetchLatestBaileysVersion();
        version = result.version;
        console.log(`[Sender] Using WA version: ${version.join('.')}`);
    } catch (err) {
        console.warn('[Sender] Could not fetch latest WA version, using default.');
        version = [2, 3000, 1015901307];
    }

    const sock = makeWASocket({
        version,
        logger,
        auth: {
            creds: state.creds,
            keys: makeCacheableSignalKeyStore(state.keys, logger),
        },
        // We handle QR printing ourselves below
        printQRInTerminal: false,
    });

    sock.ev.on('creds.update', saveCreds);

    sock.ev.on('connection.update', async (update) => {
        const { connection, lastDisconnect, qr } = update;

        // Print QR code manually so it always appears in GitHub Actions logs
        if (qr) {
            console.log('\n============================');
            console.log('SCAN THIS QR CODE WITH YOUR WHATSAPP:');
            console.log('(WhatsApp > Linked Devices > Link a Device)');
            console.log('============================\n');
            qrcode.generate(qr, { small: true }, (code) => {
                process.stdout.write(code + '\n');
            });
        }

        if (connection === 'open') {
            console.log('[Sender] Connected to WhatsApp!');

            if (mode === 'link') {
                console.log('[Sender] Successfully linked! Session saved. Exiting.');
                await new Promise(resolve => setTimeout(resolve, 3000));
                process.exit(0);
            }

            if (mode === 'test' || mode === 'send') {
                try {
                    const msgFile = path.join(__dirname, 'message_to_send.txt');
                    if (!fs.existsSync(msgFile)) {
                        console.log('[Sender] No message_to_send.txt found. Nothing to send.');
                        process.exit(0);
                    }

                    const message = fs.readFileSync(msgFile, 'utf8');
                    console.log('[Sender] Message loaded successfully.');

                    const groups = mode === 'test'
                        ? ['120363370008112217@g.us']  // Savya only
                        : ['120363295006728236@g.us', '120363370008112217@g.us']; // Both

                    for (const jid of groups) {
                        console.log(`[Sender] Sending to ${jid}...`);
                        await sock.sendMessage(jid, { text: message });
                        console.log(`[Sender] Sent to ${jid}`);
                        await new Promise(resolve => setTimeout(resolve, 2000));
                    }

                    console.log('[Sender] All messages sent! Exiting.');
                    process.exit(0);
                } catch (err) {
                    console.error('[Sender] Error sending:', err);
                    process.exit(1);
                }
            }
        }

        if (connection === 'close') {
            const statusCode = lastDisconnect?.error?.output?.statusCode;
            console.log(`[Sender] Connection closed. Status code: ${statusCode}`);
            if (statusCode === DisconnectReason.loggedOut) {
                console.error('[Sender] Logged out. Please re-run Link WhatsApp workflow.');
                process.exit(1);
            } else if (mode !== 'link') {
                console.error('[Sender] Unexpected close during send/test mode. Exiting.');
                process.exit(1);
            }
        }
    });
}

main().catch(err => {
    console.error('[Sender] Fatal error:', err);
    process.exit(1);
});
