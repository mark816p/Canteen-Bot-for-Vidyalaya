const fs = require('fs');
const { Client, LocalAuth } = require('whatsapp-web.js');
const qrcode = require('qrcode-terminal');

const mode = process.argv[2]; // 'link', 'test', or 'send'

const client = new Client({
    authStrategy: new LocalAuth({ dataPath: './.wwebjs_auth' }),
    puppeteer: {
        args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage', '--disable-gpu', '--single-process']
    }
});

client.on('qr', (qr) => {
    if (mode === 'link') {
        console.log('SCAN THIS QR CODE WITH YOUR WHATSAPP TO LINK THE BOT:');
        qrcode.generate(qr, { small: true });
    }
});

client.on('ready', async () => {
    console.log('WhatsApp Client is ready!');
    
    if (mode === 'link') {
        console.log('Successfully linked! Exiting so GitHub can save the session.');
        process.exit(0);
    }

    if (mode === 'test' || mode === 'send') {
        try {
            const message = fs.readFileSync('message_to_send.txt', 'utf8');
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
                await client.sendMessage(jid, message);
                console.log(`Sent to ${jid}`);
                // slight delay to prevent spam
                await new Promise(resolve => setTimeout(resolve, 2000));
            }
            console.log('All messages sent. Exiting cleanly.');
            process.exit(0);
        } catch (err) {
            console.error('Error sending message:', err);
            process.exit(1);
        }
    }
});

client.on('auth_failure', msg => {
    console.error('Authentication failure:', msg);
    process.exit(1);
});

console.log('Initializing WhatsApp Client...');
client.initialize().then(() => console.log('Client initialization promise resolved.')).catch(err => {
    console.error('Error during initialization:', err);
    process.exit(1);
});
