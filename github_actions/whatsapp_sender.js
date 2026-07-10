const fs = require('fs');
const path = require('path');
const qrcode = require('qrcode-terminal');
const pino = require('pino');

// Suppress verbose Baileys/pino logs
const logger = pino({ level: 'silent' });

const mode = process.argv[2]; // 'link', 'test', or 'send'
const AUTH_DIR = path.join(__dirname, '.baileys_auth');

async function main() {
    // Baileys is an ESM module, so we import it dynamically
    const {
        default: makeWASocket,
        useMultiFileAuthState,
        DisconnectReason,
        fetchLatestBaileysVersion,
        makeCacheableSignalKeyStore
    } = await import('@whiskeysockets/baileys');

    const { state, saveCreds } = await useMultiFileAuthState(AUTH_DIR);
    const { version } = await fetchLatestBaileysVersion();

    console.log(`Using Baileys v${version.join('.')}`);

    const sock = makeWASocket({
        version,
        logger,
        auth: {
            creds: state.creds,
            keys: makeCacheableSignalKeyStore(state.keys, logger),
        },
        printQRInTerminal: mode === 'link',
    });

    sock.ev.on('creds.update', saveCreds);

    sock.ev.on('connection.update', async (update) => {
        const { connection, lastDisconnect, qr } = update;

        if (mode === 'link' && qr) {
            console.log('SCAN THIS QR CODE WITH WHATSAPP (Linked Devices > Link a Device):');
        }

        if (connection === 'open') {
            console.log('Connected to WhatsApp!');

            if (mode === 'link') {
                console.log('Successfully linked! Session saved. Exiting.');
                await new Promise(resolve => setTimeout(resolve, 3000));
                process.exit(0);
            }

            if (mode === 'test' || mode === 'send') {
                try {
                    const msgFile = path.join(__dirname, 'message_to_send.txt');
                    if (!fs.existsSync(msgFile)) {
                        console.log('No message_to_send.txt found. Nothing to send (holiday or no menu).');
                        process.exit(0);
                    }

                    const message = fs.readFileSync(msgFile, 'utf8');
                    console.log('Message loaded successfully.');

                    let groups = [];
                    if (mode === 'test') {
                        groups = ['120363370008112217@g.us']; // Savya only
                    } else {
                        groups = [
                            '120363295006728236@g.us', // Navrachana Sama Canteen
                            '120363370008112217@g.us'  // Savya
                        ];
                    }

                    for (const jid of groups) {
                        console.log(`Sending message to ${jid}...`);
                        await sock.sendMessage(jid, { text: message });
                        console.log(`Successfully sent to ${jid}`);
                        await new Promise(resolve => setTimeout(resolve, 2000));
                    }

                    console.log('All messages sent successfully! Exiting.');
                    process.exit(0);
                } catch (err) {
                    console.error('Error sending message:', err);
                    process.exit(1);
                }
            }
        }

        if (connection === 'close') {
            const statusCode = lastDisconnect?.error?.output?.statusCode;
            if (statusCode === DisconnectReason.loggedOut) {
                console.error('Logged out from WhatsApp. Please re-run the Link WhatsApp workflow.');
                process.exit(1);
            } else if (mode === 'link') {
                // During linking, a close before open is expected while loading
                console.log('Connection closed during linking, waiting...');
            } else {
                console.error('Connection closed unexpectedly. Status:', statusCode);
                process.exit(1);
            }
        }
    });
}

main().catch(err => {
    console.error('Fatal error:', err);
    process.exit(1);
});
